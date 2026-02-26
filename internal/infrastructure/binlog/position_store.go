// Package binlog implementa el adaptador de infraestructura para consumir
// el Binary Log de MariaDB usando go-mysql replication.
package binlog

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/ccip/go-service/internal/domain"
	"go.uber.org/zap"
)

// FilePositionStore implementa domain.PositionRepository usando un archivo JSON
// en disco. Es suficiente para un solo nodo. Para multi-nodo usar Redis.
//
// Estrategia de escritura: cada Save() sobreescribe el archivo completo con
// una escritura atómica (write to temp + rename) para evitar corrupción
// si el proceso se interrumpe durante la escritura.
type FilePositionStore struct {
	mu       sync.Mutex
	filePath string
	log      *zap.Logger
}

// NewFilePositionStore crea un FilePositionStore en la ruta indicada.
// Crea los directorios padre si no existen.
func NewFilePositionStore(filePath string, log *zap.Logger) (*FilePositionStore, error) {
	dir := filepath.Dir(filePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("no se puede crear directorio para position store '%s': %w", dir, err)
	}
	return &FilePositionStore{filePath: filePath, log: log}, nil
}

// Save persiste la posición del binlog de forma atómica usando write+rename.
func (s *FilePositionStore) Save(_ context.Context, pos domain.BinlogPosition) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := json.Marshal(pos)
	if err != nil {
		return fmt.Errorf("error serializando posición: %w", err)
	}

	// Escribir a archivo temporal primero (misma partición para rename atómico)
	tmpFile := s.filePath + ".tmp"
	if err := os.WriteFile(tmpFile, data, 0644); err != nil {
		return fmt.Errorf("error escribiendo posición temporal: %w", err)
	}

	// Rename atómico: si el proceso muere aquí, el archivo original queda intacto
	if err := os.Rename(tmpFile, s.filePath); err != nil {
		return fmt.Errorf("error aplicando posición (rename): %w", err)
	}

	s.log.Debug("posición binlog guardada",
		zap.String("file", pos.File),
		zap.Uint32("position", pos.Position),
	)
	return nil
}

// Load recupera la última posición persistida.
// Devuelve una posición cero si el archivo no existe (primera ejecución).
func (s *FilePositionStore) Load(_ context.Context) (domain.BinlogPosition, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	data, err := os.ReadFile(s.filePath)
	if err != nil {
		if os.IsNotExist(err) {
			s.log.Info("no hay posición guardada, iniciando desde el binlog actual")
			return domain.BinlogPosition{}, nil
		}
		return domain.BinlogPosition{}, fmt.Errorf("error leyendo posición del binlog: %w", err)
	}

	var pos domain.BinlogPosition
	if err := json.Unmarshal(data, &pos); err != nil {
		// Archivo corrupto: renombrar y empezar de cero
		s.log.Warn("archivo de posición corrupto, eliminando y empezando de cero",
			zap.String("path", s.filePath),
			zap.Error(err),
		)
		_ = os.Rename(s.filePath, s.filePath+".corrupted")
		return domain.BinlogPosition{}, nil
	}

	s.log.Info("posición binlog restaurada",
		zap.String("file", pos.File),
		zap.Uint32("position", pos.Position),
	)
	return pos, nil
}
