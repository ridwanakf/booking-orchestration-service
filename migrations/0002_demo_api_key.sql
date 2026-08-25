-- +goose Up
-- A key for the local stack so the documented curl commands run against a fresh
-- clone. The secret is public by construction and is not a credential.
INSERT INTO distributor_api_keys (key_id, distributor_id, secret_hash, label)
VALUES (
    'demo01',
    'distributor-001',
    '$argon2id$v=19$m=65536,t=1,p=4$kEUDpsHWA2ANZuTEzUzSJA$e45Bk3/npTqoADREFvBvlPhArFsZPPf2Qm4+ccL1dDA',
    'local demo key'
);

-- +goose Down
DELETE FROM distributor_api_keys WHERE key_id = 'demo01';
