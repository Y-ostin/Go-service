-- ─────────────────────────────────────────────────────────────────────────────
-- Inicialización del usuario de replicación para el Go Binlog Service.
-- Este script se ejecuta automáticamente al crear el contenedor de MariaDB.
-- ─────────────────────────────────────────────────────────────────────────────

-- Crear usuario dedicado para replicación.
-- NUNCA usar root para replicación en producción.
-- El host '%' permite conexión desde cualquier IP (en prod restringir al IP del Go service).
CREATE USER IF NOT EXISTS 'replicator'@'%' IDENTIFIED BY 'repl_password_seguro';

-- El privilegio REPLICATION SLAVE es el ÚNICO necesario para leer el binlog.
-- No necesita SELECT, INSERT, ni ningún otro privilegio de datos.
GRANT REPLICATION SLAVE ON *.* TO 'replicator'@'%';

-- Aplicar cambios de privilegios inmediatamente.
FLUSH PRIVILEGES;

-- ─────────────────────────────────────────────────────────────────────────────
-- Verificación: ejecutar manualmente para confirmar configuración del binlog.
-- ─────────────────────────────────────────────────────────────────────────────
-- SHOW MASTER STATUS;                    → muestra archivo y posición actual
-- SHOW VARIABLES LIKE 'binlog_format';   → debe ser ROW
-- SHOW VARIABLES LIKE 'server_id';       → debe ser 1
-- SHOW GRANTS FOR 'replicator'@'%';     → debe mostrar REPLICATION SLAVE
