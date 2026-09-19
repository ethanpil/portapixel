-- The schema of the fleet server. Plan section 12 names the tables. This file
-- is the truth about the columns.
--
-- Rules for this file:
--  * A time value is a UTC string in RFC 3339 form. An empty string or NULL
--    means "never".
--  * A day list is a comma-separated string of "mon".."sun". An empty string
--    means "every day".
--  * A boolean is 0 or 1.
--  * A secret is never here in plain form. The server keeps the SHA-256 of a
--    device token and of an enrollment token.

CREATE TABLE groups (
  id                  INTEGER PRIMARY KEY,
  name                TEXT    NOT NULL UNIQUE,
  -- The playlist that plays when no rule of this group matches.
  default_playlist_id INTEGER REFERENCES playlists(id) ON DELETE SET NULL,
  -- The screen power rule of the group (D26). All three empty means "no rule".
  screen_on           TEXT    NOT NULL DEFAULT '',
  screen_off          TEXT    NOT NULL DEFAULT '',
  screen_days         TEXT    NOT NULL DEFAULT '',
  created_at          TEXT    NOT NULL
);

CREATE TABLE devices (
  id                   TEXT    PRIMARY KEY,               -- px-xxxxxxxx
  name                 TEXT    NOT NULL DEFAULT '',
  group_id             INTEGER REFERENCES groups(id) ON DELETE SET NULL,
  -- token_hash is the SHA-256 of the device token as lower case hex. An empty
  -- value means that this device holds no token: it waits for approval.
  token_hash           TEXT    NOT NULL DEFAULT '',
  -- claim_secret is what a pending device sends to ask "am I approved yet".
  claim_secret         TEXT    NOT NULL DEFAULT '',
  hardware_id          TEXT    NOT NULL DEFAULT '',
  -- pending is 1 while the admin has not approved this device.
  pending              INTEGER NOT NULL DEFAULT 0,
  -- pending_code is the 6-character code of the code pairing flow (D25).
  pending_code         TEXT    NOT NULL DEFAULT '',
  -- needs_confirm is 1 after the hardware ID changed (D21). prev_hardware_id
  -- holds the value that the device had before.
  needs_confirm        INTEGER NOT NULL DEFAULT 0,
  prev_hardware_id     TEXT    NOT NULL DEFAULT '',
  -- conflict is 1 when two hardware IDs used one token in a short time (D21).
  -- The server never resolves a conflict by itself.
  conflict             INTEGER NOT NULL DEFAULT 0,
  conflict_hardware_id TEXT    NOT NULL DEFAULT '',
  version              TEXT    NOT NULL DEFAULT '',
  status_json          TEXT    NOT NULL DEFAULT '',       -- the last manifest.Status
  sync_error           TEXT    NOT NULL DEFAULT '',
  last_ip              TEXT    NOT NULL DEFAULT '',
  -- poll_seconds of 0 means "use the server default".
  poll_seconds         INTEGER NOT NULL DEFAULT 0,
  -- The per-device overrides. They win over the group values.
  default_playlist_id  INTEGER REFERENCES playlists(id) ON DELETE SET NULL,
  screen_on            TEXT    NOT NULL DEFAULT '',
  screen_off           TEXT    NOT NULL DEFAULT '',
  screen_days          TEXT    NOT NULL DEFAULT '',
  created_at           TEXT    NOT NULL,
  paired_at            TEXT    NOT NULL DEFAULT '',
  last_seen            TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX devices_token ON devices(token_hash);
CREATE INDEX devices_claim ON devices(claim_secret);
CREATE INDEX devices_code  ON devices(pending_code);

CREATE TABLE enrollment_tokens (
  id         INTEGER PRIMARY KEY,
  name       TEXT    NOT NULL DEFAULT '',
  token_hash TEXT    NOT NULL UNIQUE,
  -- prefix holds the first characters of the token. The admin sees the full
  -- value one time only, so the prefix is how the list tells two tokens apart.
  prefix     TEXT    NOT NULL,
  mode       TEXT    NOT NULL,                            -- auto | pending
  group_id   INTEGER REFERENCES groups(id) ON DELETE SET NULL,
  expires_at TEXT    NOT NULL DEFAULT '',                 -- empty = no expiry
  max_uses   INTEGER NOT NULL DEFAULT 0,                  -- 0 = no limit
  uses       INTEGER NOT NULL DEFAULT 0,
  revoked    INTEGER NOT NULL DEFAULT 0,
  created_at TEXT    NOT NULL
);

CREATE TABLE media (
  sha256      TEXT    PRIMARY KEY,
  orig_name   TEXT    NOT NULL,
  size        INTEGER NOT NULL,
  mime        TEXT    NOT NULL DEFAULT '',
  width       INTEGER NOT NULL DEFAULT 0,
  height      INTEGER NOT NULL DEFAULT 0,
  has_thumb   INTEGER NOT NULL DEFAULT 0,
  uploaded_at TEXT    NOT NULL
);

CREATE TABLE playlists (
  id         INTEGER PRIMARY KEY,
  name       TEXT    NOT NULL UNIQUE,                     -- directory-safe slug
  title      TEXT    NOT NULL DEFAULT '',
  transition TEXT    NOT NULL DEFAULT '',
  -- shuffle of -1 means "the device decides". 0 and 1 are the two answers.
  shuffle    INTEGER NOT NULL DEFAULT -1,
  updated_at TEXT    NOT NULL
);

CREATE TABLE playlist_items (
  id              INTEGER PRIMARY KEY,
  playlist_id     INTEGER NOT NULL REFERENCES playlists(id) ON DELETE CASCADE,
  position        INTEGER NOT NULL,
  -- Exactly one of media_sha and url holds a value. media_sha is NULL for a url
  -- item: an empty string is not NULL, and the foreign key would then look for a
  -- media row whose hash is the empty string.
  media_sha       TEXT    REFERENCES media(sha256),
  url             TEXT    NOT NULL DEFAULT '',
  name            TEXT    NOT NULL DEFAULT '',
  duration        INTEGER NOT NULL DEFAULT 0,
  mute            INTEGER NOT NULL DEFAULT 0,
  max_duration    INTEGER NOT NULL DEFAULT 0,
  refresh_seconds INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX playlist_items_playlist ON playlist_items(playlist_id, position);
CREATE INDEX playlist_items_media    ON playlist_items(media_sha);

CREATE TABLE assignments (
  id          INTEGER PRIMARY KEY,
  -- One of group_id and device_id holds a value. A device assignment wins over
  -- a group assignment.
  group_id    INTEGER REFERENCES groups(id) ON DELETE CASCADE,
  device_id   TEXT    REFERENCES devices(id) ON DELETE CASCADE,
  playlist_id INTEGER NOT NULL REFERENCES playlists(id) ON DELETE CASCADE,
  days        TEXT    NOT NULL DEFAULT '',
  start       TEXT    NOT NULL DEFAULT '',                -- "HH:MM"
  end         TEXT    NOT NULL DEFAULT '',
  -- The rules go to the device in priority order, lowest number first. That
  -- order is the order of the rows in the admin UI, and the first rule that
  -- matches wins on the device.
  priority    INTEGER NOT NULL DEFAULT 0
);
CREATE INDEX assignments_group  ON assignments(group_id);
CREATE INDEX assignments_device ON assignments(device_id);

CREATE TABLE commands (
  id           INTEGER PRIMARY KEY,
  device_id    TEXT    NOT NULL REFERENCES devices(id) ON DELETE CASCADE,
  type         TEXT    NOT NULL,
  args_json    TEXT    NOT NULL DEFAULT '',
  queued_at    TEXT    NOT NULL,
  delivered_at TEXT    NOT NULL DEFAULT '',
  acked_at     TEXT    NOT NULL DEFAULT ''
);
CREATE INDEX commands_device ON commands(device_id, id);

CREATE TABLE releases (
  version      TEXT    PRIMARY KEY,
  approved     INTEGER NOT NULL DEFAULT 0,
  mirrored     INTEGER NOT NULL DEFAULT 0,
  notes        TEXT    NOT NULL DEFAULT '',
  published_at TEXT    NOT NULL DEFAULT '',
  approved_at  TEXT    NOT NULL DEFAULT '',
  -- mirror_state is one of idle, working, done, failed.
  mirror_state TEXT    NOT NULL DEFAULT 'idle',
  mirror_error TEXT    NOT NULL DEFAULT ''
);

CREATE TABLE settings (
  k TEXT PRIMARY KEY,
  v TEXT NOT NULL
);
