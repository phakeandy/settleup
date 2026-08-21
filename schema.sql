CREATE TABLE products (
    id          VARCHAR(64) PRIMARY KEY,
    name        VARCHAR(255) NOT NULL,
    price_cent  BIGINT NOT NULL
);

CREATE TABLE inventories (
    product_id  VARCHAR(64) PRIMARY KEY,
    available   BIGINT NOT NULL,
    updated_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
);

CREATE TABLE orders (
    id               VARCHAR(64) PRIMARY KEY,
    user_id          VARCHAR(64) NOT NULL,
    product_id       VARCHAR(64) NOT NULL,
    quantity         BIGINT NOT NULL,
    amount_cent      BIGINT NOT NULL,
    status           VARCHAR(32) NOT NULL,          -- created / paid / cancelled
    idempotency_key  VARCHAR(128) NOT NULL UNIQUE,
    created_at       TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at       TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    paid_at          TIMESTAMP NULL,
    cancelled_at     TIMESTAMP NULL,
    KEY idx_orders_status_created_at (status, created_at)
);

CREATE TABLE payments (
    id               VARCHAR(64) PRIMARY KEY,
    order_id         VARCHAR(64) NOT NULL,
    user_id          VARCHAR(64) NOT NULL,
    amount_cent      BIGINT NOT NULL,
    status           VARCHAR(32) NOT NULL,
    idempotency_key  VARCHAR(128) NOT NULL UNIQUE,
    created_at       TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    KEY idx_payments_order_id (order_id)
);

CREATE TABLE ledger_entries (
    id          VARCHAR(64) PRIMARY KEY,
    order_id    VARCHAR(64) NOT NULL,
    user_id     VARCHAR(64) NOT NULL,
    amount_cent BIGINT NOT NULL,
    direction   VARCHAR(16) NOT NULL,               -- debit / credit
    created_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    KEY idx_ledger_order_id (order_id)
);

CREATE TABLE outbox_events (
    id              VARCHAR(64) PRIMARY KEY,
    topic           VARCHAR(128) NOT NULL,
    event_key       VARCHAR(128) NOT NULL,
    payload         JSON NOT NULL,
    status          VARCHAR(32) NOT NULL,           -- pending/queued/retrying/published/dead
    retry_count     INT DEFAULT 0,
    next_attempt_at TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    last_error      VARCHAR(512) DEFAULT '',
    created_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP,
    updated_at      TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    KEY idx_outbox_status_next_attempt (status, next_attempt_at)
);

CREATE TABLE outbox_dead_letters (
    id                 VARCHAR(64) PRIMARY KEY,
    original_event_id  VARCHAR(64) NOT NULL UNIQUE,
    topic              VARCHAR(128) NOT NULL,
    event_key          VARCHAR(128) NOT NULL,
    payload            JSON NOT NULL,
    retry_count        INT NOT NULL,
    last_error         VARCHAR(512) NOT NULL,
    created_at         TIMESTAMP DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE reconciliation_issues (
    id          VARCHAR(64) PRIMARY KEY,
    issue_key   VARCHAR(255) NOT NULL UNIQUE,
    issue_type  VARCHAR(128) NOT NULL,
    subject_type VARCHAR(64) NOT NULL,
    subject_id  VARCHAR(128) NOT NULL,
    detail      VARCHAR(1024) NOT NULL,
    status      VARCHAR(32) NOT NULL,
    detected_at TIMESTAMP NOT NULL,
    updated_at  TIMESTAMP DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP
);
