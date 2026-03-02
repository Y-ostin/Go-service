package binlog

import (
	"context"
	"fmt"
	"strings"
	"time"

	gmclient "github.com/go-mysql-org/go-mysql/client"
	gm "github.com/go-mysql-org/go-mysql/mysql"
	"github.com/go-mysql-org/go-mysql/replication"
	"github.com/google/uuid"
	"go.uber.org/zap"

	"github.com/ccip/go-service/internal/config"
	"github.com/ccip/go-service/internal/domain"
	"github.com/ccip/go-service/pkg/metrics"
)

// Listener es el adaptador de infraestructura que conecta al replication stream
// de MariaDB y convierte los raw events del binlog en domain.BinlogEvent.
//
// Gestiona automáticamente:
//   - Reconexión con backoff exponencial
//   - Persistencia de posición en cada evento
//   - Filtrado de tablas/schemas
//   - Transformación de filas a estructuras de dominio
type Listener struct {
	cfg          *config.Config
	positionRepo domain.PositionRepository
	publisher    domain.EventPublisher
	metrics      *metrics.Metrics
	log          *zap.Logger

	// watchedSet es el set de "schema.table" en lowercase para búsqueda O(1).
	// Si está vacío se procesan todas las tablas de los schemas configurados.
	watchedSet map[string]struct{}

	// watchedSchemas es el set de schemas en lowercase.
	watchedSchemas map[string]struct{}
}

// NewListener construye el Listener con todas sus dependencias.
func NewListener(
	cfg *config.Config,
	posRepo domain.PositionRepository,
	pub domain.EventPublisher,
	m *metrics.Metrics,
	log *zap.Logger,
) *Listener {

	l := &Listener{
		cfg:          cfg,
		positionRepo: posRepo,
		publisher:    pub,
		metrics:      m,
		log:          log,
		watchedSet:   make(map[string]struct{}),
		watchedSchemas: make(map[string]struct{}),
	}

	// Construir sets de búsqueda O(1) para filtrado de tablas/schemas
	for _, t := range cfg.Binlog.WatchedTables {
		l.watchedSet[strings.ToLower(t)] = struct{}{}
	}
	for _, s := range cfg.Binlog.WatchedSchemas {
		l.watchedSchemas[strings.ToLower(s)] = struct{}{}
	}

	return l
}

// Start inicia el consumo del binlog con reconexión automática.
// Bloquea hasta que ctx sea cancelado.
func (l *Listener) Start(ctx context.Context) error {
	l.log.Info("iniciando binlog listener",
		zap.String("host", l.cfg.DB.Host),
		zap.Uint16("port", l.cfg.DB.Port),
		zap.Uint32("server_id", l.cfg.DB.ServerID),
	)

	// Backoff: 1s → 2s → 4s → 8s → 16s → 30s (cap)
	backoff := time.Second
	maxBackoff := 30 * time.Second

	for {
		select {
		case <-ctx.Done():
			l.log.Info("binlog listener detenido por context cancel")
			return nil
		default:
		}

		err := l.runOnce(ctx)
		if err == nil {
			// runOnce retornó sin error = context cancelado limpiamente
			return nil
		}

		l.metrics.BinlogReconnections.Inc()
		l.log.Warn("error en el stream del binlog, reconectando",
			zap.Error(err),
			zap.Duration("backoff", backoff),
		)

		// Esperar con backoff antes de reconectar
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(backoff):
		}

		backoff *= 2
		if backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// runOnce establece UNA conexión al binlog y lee hasta error o context cancel.
func (l *Listener) runOnce(ctx context.Context) error {
	cfg := replication.BinlogSyncerConfig{
		ServerID:        l.cfg.DB.ServerID,
		Flavor:          l.cfg.Binlog.Flavor,
		Host:            l.cfg.DB.Host,
		Port:            l.cfg.DB.Port,
		User:            l.cfg.DB.User,
		Password:        l.cfg.DB.Password,
		// Importante: deshabilitar heartbeat solo para debug, en prod dejarlo activado
		HeartbeatPeriod: 30 * time.Second,
		// RawModeEnabled: false → go-mysql parsea los eventos por nosotros
		RawModeEnabled: false,
		// UseDecimal: true → valores DECIMAL vienen como string preciso (no float64)
		UseDecimal: true,
	}

	syncer := replication.NewBinlogSyncer(cfg)
	defer syncer.Close()

	// Recuperar posición guardada para reanudar
	pos, err := l.positionRepo.Load(ctx)
	if err != nil {
		return fmt.Errorf("error cargando posición: %w", err)
	}

	var streamer *replication.BinlogStreamer

	if pos.IsZero() {
		// Primera ejecución: consultar SHOW MASTER STATUS para obtener posición actual real
		masterPos, posErr := l.getMasterPosition()
		if posErr != nil {
			return fmt.Errorf("error obteniendo posición del master: %w", posErr)
		}
		l.log.Info("primera ejecución, iniciando desde posición actual del master",
			zap.String("file", masterPos.Name),
			zap.Uint32("pos", masterPos.Pos),
		)
		streamer, err = syncer.StartSync(masterPos)
	} else {
		l.log.Info("reanudando desde posición guardada",
			zap.String("file", pos.File),
			zap.Uint32("position", pos.Position),
		)
		streamer, err = syncer.StartSync(gm.Position{
			Name: pos.File,
			Pos:  pos.Position,
		})
	}

	if err != nil {
		return fmt.Errorf("error iniciando binlog sync: %w", err)
	}

	l.log.Info("conexión al binlog establecida exitosamente")

	// Variable que acumula la tabla del evento de fila actual.
	// Los RowEvents no incluyen el nombre de la tabla directamente,
	// sino que referencian el último TableMapEvent recibido.
	tableMap := make(map[uint64]*replication.TableMapEvent)

	// saveTicker: persistir posición cada N segundos (debounce)
	saveTicker := time.NewTicker(l.cfg.Position.SaveInterval)
	defer saveTicker.Stop()

	var lastPos domain.BinlogPosition

	for {
		// GetEvent con timeout para poder verificar el contexto regularmente
		evtCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
		rawEvt, err := streamer.GetEvent(evtCtx)
		cancel()

		if err != nil {
			if ctx.Err() != nil {
				// Context cancelado limpiamente
				return nil
			}
			// Timeout (normal, sin eventos en 5s) o error real
			if err.Error() == "context deadline exceeded" {
				continue // sin eventos, seguir esperando
			}
			return fmt.Errorf("error obteniendo evento del binlog: %w", err)
		}

		// Persistir posición en ticker (no en cada evento para no saturar disco)
		select {
		case <-saveTicker.C:
			if !lastPos.IsZero() {
				_ = l.positionRepo.Save(ctx, lastPos)
			}
		default:
		}

		// Procesar el evento
		l.processEvent(ctx, rawEvt, tableMap, &lastPos)
	}
}

// getMasterPosition consulta SHOW MASTER STATUS en MariaDB y devuelve la posición actual.
func (l *Listener) getMasterPosition() (gm.Position, error) {
	addr := fmt.Sprintf("%s:%d", l.cfg.DB.Host, l.cfg.DB.Port)
	conn, err := gmclient.Connect(addr, l.cfg.DB.User, l.cfg.DB.Password, "")
	if err != nil {
		return gm.Position{}, fmt.Errorf("connect para SHOW MASTER STATUS: %w", err)
	}
	defer conn.Close()

	res, err := conn.Execute("SHOW MASTER STATUS")
	if err != nil {
		return gm.Position{}, fmt.Errorf("SHOW MASTER STATUS: %w", err)
	}
	if len(res.Values) == 0 {
		return gm.Position{}, fmt.Errorf("SHOW MASTER STATUS devolvió vacío — ¿binlog habilitado?")
	}

	row := res.Values[0]
	file := string(row[0].AsString())
	pos := uint32(row[1].AsUint64())
	return gm.Position{Name: file, Pos: pos}, nil
}


// processEvent despacha el raw event al handler correcto según su tipo.
func (l *Listener) processEvent(
	ctx context.Context,
	evt *replication.BinlogEvent,
	tableMap map[uint64]*replication.TableMapEvent,
	lastPos *domain.BinlogPosition,
) {
	// Actualizar posición después de cada evento (para el periodic save)
	header := evt.Header
	if header.LogPos > 0 {
		// LogPos apunta al FINAL del evento actual = inicio del próximo
		// El nombre del archivo se mantiene hasta que llega un RotateEvent
		if lastPos.File != "" {
			lastPos.Position = header.LogPos
		}
	}

	switch e := evt.Event.(type) {

	// RotateEvent indica cambio de archivo binlog
	case *replication.RotateEvent:
		newFile := string(e.NextLogName)
		l.log.Info("binlog rotado",
			zap.String("new_file", newFile),
			zap.Uint64("position", e.Position),
		)
		lastPos.File = newFile
		lastPos.Position = uint32(e.Position)

		// Guardar inmediatamente en rotación
		_ = l.positionRepo.Save(ctx, *lastPos)

	// TableMapEvent define el mapeo de tableID → schema+table
	// DEBE procesarse ANTES de los RowEvents
	case *replication.TableMapEvent:
		tableMap[e.TableID] = e

	// RowsEvent cubre INSERT/UPDATE/DELETE en formato ROW
	case *replication.RowsEvent:
		tme, ok := tableMap[e.TableID]
		if !ok {
			l.log.Warn("TableMapEvent no encontrado para evento de filas",
				zap.Uint64("table_id", e.TableID),
			)
			return
		}

		schema := strings.ToLower(string(tme.Schema))
		table := strings.ToLower(string(tme.Table))

		// Filtrar: solo procesar tablas/schemas configurados
		if !l.shouldProcess(schema, table) {
			return
		}

		l.handleRowsEvent(evt, e, tme, header.Timestamp)

	case *replication.QueryEvent:
		// DDL y BEGIN/COMMIT en statement mode. En ROW mode solo vemos DDL.
		// Ignorar para nuestro propósito.

	case *replication.XIDEvent:
		// Fin de transacción. Útil para idempotencia con XID.
		// No necesario en nuestro modelo event-per-row.
	}
}

// shouldProcess determina si una tabla debe procesarse según la configuración.
func (l *Listener) shouldProcess(schema, table string) bool {
	// Si hay tablas específicas configuradas, verificar el set
	if len(l.watchedSet) > 0 {
		_, ok := l.watchedSet[schema+"."+table]
		return ok
	}

	// Si hay schemas configurados, verificar el schema
	if len(l.watchedSchemas) > 0 {
		_, ok := l.watchedSchemas[schema]
		return ok
	}

	// Sin filtros = procesar todo
	return true
}

// handleRowsEvent convierte un RowsEvent del binlog en uno o más domain.BinlogEvent.
// Un solo RowsEvent puede contener MÚLTIPLES filas modificadas en la misma transacción.
func (l *Listener) handleRowsEvent(
	rawEvt *replication.BinlogEvent,
	e *replication.RowsEvent,
	tme *replication.TableMapEvent,
	unixTimestamp uint32,
) {
	eventType := rawEventTypeToDomain(rawEvt.Header.EventType)
	if eventType == "" {
		return // tipo no soportado
	}

	schema := string(tme.Schema)
	table := string(tme.Table)
	ts := time.Unix(int64(unixTimestamp), 0)

	// En UPDATE: rows viene intercalado [before1, after1, before2, after2, ...]
	// En INSERT/DELETE: rows es [row1, row2, ...]
	rows := e.Rows

	switch eventType {
	case domain.EventInsert:
		for _, row := range rows {
			after := rowToMap(tme.ColumnName, row)
			l.emit(eventType, schema, table, nil, after, rawEvt, ts)
		}

	case domain.EventDelete:
		for _, row := range rows {
			before := rowToMap(tme.ColumnName, row)
			l.emit(eventType, schema, table, before, nil, rawEvt, ts)
		}

	case domain.EventUpdate:
		// Rows viene en pares: [old_row, new_row, old_row, new_row, ...]
		for i := 0; i+1 < len(rows); i += 2 {
			before := rowToMap(tme.ColumnName, rows[i])
			after := rowToMap(tme.ColumnName, rows[i+1])
			l.emit(eventType, schema, table, before, after, rawEvt, ts)
		}
	}
}

// emit construye el domain.BinlogEvent y lo publica al EventPublisher.
func (l *Listener) emit(
	eventType domain.EventType,
	schema, table string,
	before, after domain.RowData,
	rawEvt *replication.BinlogEvent,
	ts time.Time,
) {
	evt := &domain.BinlogEvent{
		ID:         uuid.NewString(), // ID único para idempotencia en el cliente
		Type:       eventType,
		Schema:     schema,
		Table:      table,
		Before:     before,
		After:      after,
		BinlogFile: "", // se completará en el dispatcher si se necesita
		BinlogPos:  rawEvt.Header.LogPos,
		Timestamp:  ts,
		ReceivedAt: time.Now(),
	}

	// Medir lag binlog
	lag := time.Since(ts).Seconds()
	l.metrics.BinlogLagSeconds.Set(lag)
	l.metrics.BinlogEventsTotal.WithLabelValues(
		string(eventType), schema, table,
	).Inc()

	l.publisher.Publish(evt)
}

// rowToMap convierte una slice de valores raw en un RowData con nombres de columna.
// Si ColumnName no está disponible (versiones antiguas del binlog), usa índices.
func rowToMap(columnNames [][]byte, row []interface{}) domain.RowData {
	result := make(domain.RowData, len(row))
	for i, val := range row {
		var key string
		if i < len(columnNames) && len(columnNames[i]) > 0 {
			key = string(columnNames[i])
		} else {
			key = fmt.Sprintf("col_%d", i)
		}
		result[key] = normalizeValue(val)
	}
	return result
}

// normalizeValue convierte tipos especiales del binlog a tipos JSON-serializables.
func normalizeValue(v interface{}) interface{} {
	switch val := v.(type) {
	case []byte:
		// BLOB/BINARY/TEXT en el binlog viene como []byte
		return string(val)
	case nil:
		return nil
	default:
		return val
	}
}

// rawEventTypeToDomain mapea los tipos internos de go-mysql a domain.EventType.
func rawEventTypeToDomain(t replication.EventType) domain.EventType {
	switch t {
	case replication.WRITE_ROWS_EVENTv0,
		replication.WRITE_ROWS_EVENTv1,
		replication.WRITE_ROWS_EVENTv2:
		return domain.EventInsert

	case replication.UPDATE_ROWS_EVENTv0,
		replication.UPDATE_ROWS_EVENTv1,
		replication.UPDATE_ROWS_EVENTv2:
		return domain.EventUpdate

	case replication.DELETE_ROWS_EVENTv0,
		replication.DELETE_ROWS_EVENTv1,
		replication.DELETE_ROWS_EVENTv2:
		return domain.EventDelete

	default:
		return ""
	}
}
