// Package mqtt implementa el publicador de KPIs hacia el broker EMQX.
//
// Flujo:
//   BinlogEvent (tabla relevante)
//     → debounce 800 ms
//     → GET http://localhost:{serverPort}/api/kpis?year=Y&month=M
//     → Publish JSON al topic /data (MQTT TLS port 8883)
//
// El dashboard (browser) se suscribe al mismo topic vía MQTT-over-WebSocket (WSS).
package mqtt

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"go.uber.org/zap"

	"github.com/ccip/go-service/internal/config"
	"github.com/ccip/go-service/internal/domain"
)

// watchedTables son las tablas cuyo cambio dispara re-publicación de KPIs.
var watchedTables = map[string]bool{
	"cicsa_charge_areas": true,
	"payment_approvals":  true,
}

// Publisher publica KPIs financieros al broker MQTT (EMQX Cloud).
// Implementa domain.EventPublisher para conectarse al dispatcher.
type Publisher struct {
	cfg        *config.MQTTConfig
	serverPort int
	log        *zap.Logger
	client     paho.Client

	mu    sync.Mutex
	timer *time.Timer
}

// NewPublisher construye y conecta el cliente MQTT al broker EMQX.
// Si la conexión falla retorna error — el caller debe decidir si es fatal.
func NewPublisher(cfg *config.MQTTConfig, serverPort int, log *zap.Logger) (*Publisher, error) {
	p := &Publisher{
		cfg:        cfg,
		serverPort: serverPort,
		log:        log,
	}

	tlsCfg, err := buildTLSConfig(cfg.CACert)
	if err != nil {
		return nil, fmt.Errorf("mqtt TLS config: %w", err)
	}

	opts := paho.NewClientOptions()
	opts.AddBroker(fmt.Sprintf("tcps://%s:%d", cfg.Broker, cfg.Port))
	opts.SetClientID(cfg.ClientID)
	opts.SetUsername(cfg.Username)
	opts.SetPassword(cfg.Password)
	opts.SetTLSConfig(tlsCfg)
	opts.SetCleanSession(true)
	opts.SetAutoReconnect(true)
	opts.SetConnectRetry(true)
	opts.SetConnectRetryInterval(5 * time.Second)
	opts.SetKeepAlive(30 * time.Second)

	opts.SetConnectionLostHandler(func(_ paho.Client, err error) {
		log.Warn("MQTT: conexión perdida, reconectando...", zap.Error(err))
	})
	opts.SetOnConnectHandler(func(_ paho.Client) {
		log.Info("MQTT: conectado al broker EMQX",
			zap.String("broker", cfg.Broker),
			zap.Int("port", cfg.Port),
			zap.String("topic", cfg.Topic),
		)
	})
	opts.SetReconnectingHandler(func(_ paho.Client, _ *paho.ClientOptions) {
		log.Info("MQTT: reconectando al broker...")
	})

	client := paho.NewClient(opts)

	token := client.Connect()
	if !token.WaitTimeout(15 * time.Second) {
		return nil, fmt.Errorf("mqtt connect timeout (15s) para %s:%d", cfg.Broker, cfg.Port)
	}
	if token.Error() != nil {
		return nil, fmt.Errorf("mqtt connect: %w", token.Error())
	}

	p.client = client
	return p, nil
}

// ── domain.EventPublisher ─────────────────────────────────────────────────────

// Publish recibe un evento binlog. Si la tabla es relevante, activa el debounce
// para publicar los KPIs actualizados al topic MQTT.
func (p *Publisher) Publish(event *domain.BinlogEvent) {
	if !watchedTables[event.Table] {
		return
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	if p.timer != nil {
		p.timer.Stop()
	}
	p.timer = time.AfterFunc(800*time.Millisecond, p.fetchAndPublish)
}

// ClientCount devuelve 1 si hay conexión activa, 0 si no.
func (p *Publisher) ClientCount() int {
	if p.client != nil && p.client.IsConnected() {
		return 1
	}
	return 0
}

// Close desconecta el cliente MQTT limpiamente.
func (p *Publisher) Close() {
	if p.client != nil {
		p.client.Disconnect(500)
	}
}

// ── Internal ──────────────────────────────────────────────────────────────────

// fetchAndPublish consulta /api/kpis del servidor HTTP local y publica el JSON al broker.
func (p *Publisher) fetchAndPublish() {
	now := time.Now()
	url := fmt.Sprintf("http://localhost:%d/api/kpis?year=%d&month=%d",
		p.serverPort, now.Year(), int(now.Month()),
	)

	httpClient := &http.Client{Timeout: 10 * time.Second}
	resp, err := httpClient.Get(url)
	if err != nil {
		p.log.Error("MQTT: error GET /api/kpis", zap.String("url", url), zap.Error(err))
		return
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		p.log.Error("MQTT: error leyendo respuesta KPIs", zap.Error(err))
		return
	}

	if !p.client.IsConnected() {
		p.log.Warn("MQTT: cliente desconectado, omitiendo publicación")
		return
	}

	token := p.client.Publish(p.cfg.Topic, 1, false, body)
	if !token.WaitTimeout(5 * time.Second) {
		p.log.Error("MQTT: timeout publicando al topic", zap.String("topic", p.cfg.Topic))
		return
	}
	if token.Error() != nil {
		p.log.Error("MQTT: error publicando", zap.String("topic", p.cfg.Topic), zap.Error(token.Error()))
		return
	}

	p.log.Info("MQTT: KPIs publicados",
		zap.String("topic", p.cfg.Topic),
		zap.Int("bytes", len(body)),
	)
}

// buildTLSConfig crea la configuración TLS con el CA cert de EMQX.
// Si el archivo no existe, usa InsecureSkipVerify (sólo para desarrollo).
func buildTLSConfig(caCertPath string) (*tls.Config, error) {
	data, err := os.ReadFile(caCertPath)
	if err != nil {
		// CA cert no encontrado: continuar sin verificación (modo dev)
		return &tls.Config{
			InsecureSkipVerify: true, //nolint:gosec // dev fallback
		}, nil
	}

	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(data) {
		return nil, fmt.Errorf("no se pudo parsear CA cert: %s", caCertPath)
	}

	return &tls.Config{
		RootCAs: pool,
	}, nil
}
