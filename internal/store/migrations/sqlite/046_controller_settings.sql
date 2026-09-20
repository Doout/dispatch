CREATE TABLE controller_settings (
 id INTEGER PRIMARY KEY CHECK (id = 1),
 operations_enabled INTEGER NOT NULL DEFAULT 0 CHECK (operations_enabled IN (0, 1))
);
INSERT INTO controller_settings (id, operations_enabled) VALUES (1, 0);
