-- Per-server console line ending. Most servers accept a bare LF, but some
-- (Vintage Story) only treat a carriage-return/line-feed pair as Enter, so a
-- stdin stop command would never run. 'lf' keeps the historical behavior.
ALTER TABLE servers ADD COLUMN console_line_ending TEXT NOT NULL DEFAULT 'lf' CHECK (console_line_ending IN ('lf','crlf'));
