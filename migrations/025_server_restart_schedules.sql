-- Local, typed recurring restart schedules. The server foreign key makes
-- deletion of a server remove its schedules without leaving orphan rows.
CREATE TABLE server_restart_schedules (
    id TEXT PRIMARY KEY,
    server_id TEXT NOT NULL REFERENCES servers(id) ON DELETE CASCADE,
    enabled INTEGER NOT NULL DEFAULT 1 CHECK (enabled IN (0, 1)),
    schedule_type TEXT NOT NULL CHECK (schedule_type IN ('daily', 'weekly')),
    time_of_day TEXT NOT NULL,
    day_of_week INTEGER,
    time_zone TEXT NOT NULL,
    created_at TEXT NOT NULL,
    updated_at TEXT NOT NULL,
    CHECK ((schedule_type = 'daily' AND day_of_week IS NULL) OR
           (schedule_type = 'weekly' AND day_of_week BETWEEN 0 AND 6))
);

CREATE INDEX idx_server_restart_schedules_enabled
    ON server_restart_schedules(enabled, server_id);
CREATE INDEX idx_server_restart_schedules_server
    ON server_restart_schedules(server_id);
