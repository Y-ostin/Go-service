// Package config maneja toda la configuración del servicio mediante variables
// de entorno y archivos .env, usando viper.
package config

import (
	"fmt"
	"strings"
	"time"

	"github.com/spf13/viper"
)

// Config es la estructura principal de configuración del servicio.
type Config struct {
	Server   ServerConfig
	DB       DBConfig
	Binlog   BinlogConfig
	Position PositionConfig
	Throttle ThrottleConfig
	Log      LogConfig
}

// ServerConfig contiene la configuración del servidor HTTP.
type ServerConfig struct {
	// Port en el que escucha el servidor HTTP (WebSocket + SSE + métricas).
	Port int `mapstructure:"SERVER_PORT"`

	// ReadTimeout para conexiones HTTP entrantes.
	ReadTimeout time.Duration

	// WriteTimeout para respuestas HTTP salientes.
	WriteTimeout time.Duration

	// MaxClients limita el número máximo de clientes WebSocket/SSE simultáneos.
	MaxClients int `mapstructure:"SERVER_MAX_CLIENTS"`
}

// DBConfig contiene los parámetros de conexión a MariaDB para el replication
// y para las consultas SQL de la aplicación (dashboard).
type DBConfig struct {
	// Host del servidor MariaDB.
	Host string `mapstructure:"DB_HOST"`

	// Port del servidor MariaDB.
	Port uint16 `mapstructure:"DB_PORT"`

	// User con privilegios REPLICATION SLAVE.
	User string `mapstructure:"DB_REPL_USER"`

	// Password del usuario de replicación.
	Password string `mapstructure:"DB_REPL_PASSWORD"`

	// ServerID debe ser ÚNICO en el cluster de replicación. Nunca usar el mismo
	// que el server-id del MariaDB primario o de otros esclavos.
	ServerID uint32 `mapstructure:"DB_SERVER_ID"`

	// Name es el nombre del esquema/base de datos para las consultas del dashboard.
	Name string `mapstructure:"DB_NAME"`

	// QueryUser es el usuario MySQL para las consultas SQL del dashboard.
	// Si está vacío se usa User (replication user).
	QueryUser string `mapstructure:"DB_QUERY_USER"`

	// QueryPassword es la contraseña de QueryUser.
	// Si está vacío se usa Password.
	QueryPassword string `mapstructure:"DB_QUERY_PASSWORD"`
}

// DSN construye el Data Source Name para database/sql (go-sql-driver/mysql).
// Usa QueryUser/QueryPassword si están definidos; si no, recae en el usuario de replicación.
func (c DBConfig) DSN() string {
	user := c.QueryUser
	if user == "" {
		user = c.User
	}
	pass := c.QueryPassword
	if pass == "" {
		pass = c.Password
	}
	return fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?parseTime=true&loc=UTC",
		user, pass, c.Host, c.Port, c.Name)
}

// BinlogConfig define qué eventos procesar.
type BinlogConfig struct {
	// WatchedSchemas limita el procesamiento a esquemas específicos.
	// Si está vacío, se procesan todos.
	WatchedSchemas []string `mapstructure:"BINLOG_SCHEMAS"`

	// WatchedTables define las tablas a procesar en formato "schema.table".
	// Ejemplo: ["ccip_erp_legacy.financial_results", "ccip_erp_legacy.transactions"]
	// Si está vacío, se procesan todas las tablas de los esquemas configurados.
	WatchedTables []string `mapstructure:"BINLOG_TABLES"`

	// Flavor puede ser "mysql" o "mariadb". Para MariaDB 10.4 usar "mariadb".
	Flavor string `mapstructure:"BINLOG_FLAVOR"`

	// UseGTID habilita el modo GTID en lugar de File+Position.
	// GTID es más robusto para ambientes con failover.
	UseGTID bool `mapstructure:"BINLOG_USE_GTID"`
}

// PositionConfig define cómo se persiste la posición del binlog.
type PositionConfig struct {
	// StorageType puede ser "file" o "redis".
	StorageType string `mapstructure:"POSITION_STORAGE"`

	// FilePath ruta donde se guarda el archivo de posición (si StorageType=file).
	FilePath string `mapstructure:"POSITION_FILE_PATH"`

	// SaveInterval cada cuánto tiempo se guarda la posición (debounce).
	SaveInterval time.Duration
}

// ThrottleConfig controla el rate limiting hacia el frontend para no inundar
// el dashboard con miles de eventos por segundo.
type ThrottleConfig struct {
	// MaxEventsPerSecond limita cuántos eventos se envían al broadcast por segundo.
	// Eventos que superen este límite se agrupan en un batch.
	MaxEventsPerSecond int `mapstructure:"THROTTLE_MAX_EPS"`

	// BatchWindow define el time window para acumular eventos antes de emitirlos.
	BatchWindow time.Duration

	// BatchMaxSize es el máximo de eventos que puede contener un batch.
	BatchMaxSize int `mapstructure:"THROTTLE_BATCH_SIZE"`

	// WorkerPoolSize define cuántas goroutines procesan eventos de forma concurrente.
	WorkerPoolSize int `mapstructure:"THROTTLE_WORKERS"`
}

// LogConfig controla el logging.
type LogConfig struct {
	// Level puede ser: debug, info, warn, error.
	Level string `mapstructure:"LOG_LEVEL"`

	// Format puede ser "json" (producción) o "console" (desarrollo).
	Format string `mapstructure:"LOG_FORMAT"`
}

// Load carga la configuración desde variables de entorno y/o archivo .env.
func Load() (*Config, error) {
	v := viper.New()

	// Leer desde archivo .env si existe
	v.SetConfigName(".env")
	v.SetConfigType("env")
	v.AddConfigPath(".")
	v.AddConfigPath("/app") // dentro de Docker

	// Variables de entorno del sistema tienen prioridad
	v.AutomaticEnv()
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))

	// Leer archivo (no falla si no existe)
	_ = v.ReadInConfig()

	// ── Defaults ────────────────────────────────────────────────────────────
	v.SetDefault("SERVER_PORT", 8090)
	v.SetDefault("SERVER_MAX_CLIENTS", 500)
	v.SetDefault("DB_HOST", "127.0.0.1")
	v.SetDefault("DB_PORT", 3306)
	v.SetDefault("DB_REPL_USER", "replicator")
	v.SetDefault("DB_SERVER_ID", 100)
	v.SetDefault("DB_NAME", "ccip_erp_legacy")
	v.SetDefault("DB_QUERY_USER", "")
	v.SetDefault("DB_QUERY_PASSWORD", "")
	v.SetDefault("BINLOG_FLAVOR", "mariadb")
	v.SetDefault("BINLOG_USE_GTID", false)
	v.SetDefault("POSITION_STORAGE", "file")
	v.SetDefault("POSITION_FILE_PATH", "/tmp/binlog_position.json")
	v.SetDefault("THROTTLE_MAX_EPS", 100)
	v.SetDefault("THROTTLE_BATCH_SIZE", 50)
	v.SetDefault("THROTTLE_WORKERS", 4)
	v.SetDefault("LOG_LEVEL", "info")
	v.SetDefault("LOG_FORMAT", "json")

	cfg := &Config{}

	cfg.Server = ServerConfig{
		Port:         v.GetInt("SERVER_PORT"),
		MaxClients:   v.GetInt("SERVER_MAX_CLIENTS"),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	cfg.DB = DBConfig{
		Host:          v.GetString("DB_HOST"),
		Port:          uint16(v.GetInt("DB_PORT")),
		User:          v.GetString("DB_REPL_USER"),
		Password:      v.GetString("DB_REPL_PASSWORD"),
		ServerID:      uint32(v.GetInt("DB_SERVER_ID")),
		Name:          v.GetString("DB_NAME"),
		QueryUser:     v.GetString("DB_QUERY_USER"),
		QueryPassword: v.GetString("DB_QUERY_PASSWORD"),
	}

	cfg.Binlog = BinlogConfig{
		WatchedSchemas: v.GetStringSlice("BINLOG_SCHEMAS"),
		WatchedTables:  v.GetStringSlice("BINLOG_TABLES"),
		Flavor:         v.GetString("BINLOG_FLAVOR"),
		UseGTID:        v.GetBool("BINLOG_USE_GTID"),
	}

	cfg.Position = PositionConfig{
		StorageType:  v.GetString("POSITION_STORAGE"),
		FilePath:     v.GetString("POSITION_FILE_PATH"),
		SaveInterval: 2 * time.Second,
	}

	cfg.Throttle = ThrottleConfig{
		MaxEventsPerSecond: v.GetInt("THROTTLE_MAX_EPS"),
		BatchWindow:        50 * time.Millisecond,
		BatchMaxSize:       v.GetInt("THROTTLE_BATCH_SIZE"),
		WorkerPoolSize:     v.GetInt("THROTTLE_WORKERS"),
	}

	cfg.Log = LogConfig{
		Level:  v.GetString("LOG_LEVEL"),
		Format: v.GetString("LOG_FORMAT"),
	}

	if err := cfg.validate(); err != nil {
		return nil, fmt.Errorf("configuración inválida: %w", err)
	}

	return cfg, nil
}

func (c *Config) validate() error {
	if c.DB.User == "" {
		return fmt.Errorf("DB_REPL_USER es requerido")
	}
	if c.DB.ServerID == 0 {
		return fmt.Errorf("DB_SERVER_ID no puede ser 0")
	}
	if c.DB.ServerID == 1 {
		return fmt.Errorf("DB_SERVER_ID=1 está reservado para el MariaDB primario")
	}
	if c.Throttle.WorkerPoolSize < 1 {
		return fmt.Errorf("THROTTLE_WORKERS debe ser >= 1")
	}
	return nil
}
