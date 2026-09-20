CREATE TABLE controller_settings (
 id INTEGER PRIMARY KEY CHECK (id = 1),
 operations_enabled BOOLEAN NOT NULL DEFAULT FALSE
);
INSERT INTO controller_settings (id, operations_enabled) VALUES (1, FALSE);
