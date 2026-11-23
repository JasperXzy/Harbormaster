ALTER TABLE passkey_state ADD COLUMN password_enabled INTEGER NOT NULL DEFAULT 1 CHECK(password_enabled IN (0,1));
