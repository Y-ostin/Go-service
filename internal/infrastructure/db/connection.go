// Package db provee la conexión SQL y las implementaciones de repositorio
// para las consultas de aplicación sobre MariaDB.
package db

import (
	"database/sql"
	"fmt"
	"time"

	// Driver MySQL/MariaDB — registra el driver "mysql" en database/sql.
	_ "github.com/go-sql-driver/mysql"

	"github.com/ccip/go-service/internal/config"
)

// NewConnection crea y valida un pool de conexiones *sql.DB para MariaDB.
// Falla rápido si no puede hacer ping al servidor.
func NewConnection(cfg config.DBConfig) (*sql.DB, error) {
	db, err := sql.Open("mysql", cfg.DSN())
	if err != nil {
		return nil, fmt.Errorf("abriendo conexión a MariaDB: %w", err)
	}

	// Parámetros del pool — conservadores para un servicio de dashboard.
	db.SetMaxOpenConns(25)
	db.SetMaxIdleConns(5)
	db.SetConnMaxLifetime(5 * time.Minute)
	db.SetConnMaxIdleTime(2 * time.Minute)

	if err := db.Ping(); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("ping a MariaDB falló (host: %s:%d db: %s): %w",
			cfg.Host, cfg.Port, cfg.Name, err)
	}

	return db, nil
}
