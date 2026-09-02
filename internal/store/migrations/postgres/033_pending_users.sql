ALTER TABLE users DROP CONSTRAINT IF EXISTS users_state_check;
ALTER TABLE users
    ADD CONSTRAINT users_state_check CHECK(state IN ('active', 'pending', 'disabled'));
