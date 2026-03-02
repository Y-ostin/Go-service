package config

import (
	"fmt"
	"gorm.io/driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
	"time"
)

// DB es la instancia global de la base de datos (GORM)
var DB *gorm.DB

// ConnectDB inicializa la conexión a MySQL/MariaDB usando GORM
func ConnectDB(cfg *DBConfig) (*gorm.DB, error) {
	// Usamos query user para las consultas principales
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%d)/%s?charset=utf8mb4&parseTime=True&loc=Local",
		cfg.QueryUser,
		cfg.QueryPassword,
		cfg.Host,
		cfg.Port,
		cfg.QueryDBName,
	)

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{
		Logger: logger.Default.LogMode(logger.Info), // Log de SQL en desarrollo
	})

	if err != nil {
		return nil, fmt.Errorf("error al conectar con GORM: %w", err)
	}

	// Configurar pool de conexiones
	sqlDB, _ := db.DB()
	sqlDB.SetMaxIdleConns(10)
	sqlDB.SetMaxOpenConns(100)
	sqlDB.SetConnMaxLifetime(time.Hour)

	DB = db
	return db, nil
}
