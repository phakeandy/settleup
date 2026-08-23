CREATE TABLE products (
    id          INT          AUTO_INCREMENT PRIMARY KEY,
    name        VARCHAR(255) NOT NULL,
    price_cent  BIGINT       NOT NULL
);

CREATE TABLE orders (
    id               INT           AUTO_INCREMENT PRIMARY KEY,
    user_id          INT           NOT NULL,
    product_id       INT           NOT NULL,
    quantity         BIGINT        NOT NULL,
    amount_cent      BIGINT        NOT NULL,
    status           INT           NOT NULL,  -- 1->created / 2->paid / 3->cancelled
    idempotency_key  VARCHAR(128)  NOT NULL UNIQUE,
    created_at       TIMESTAMP     DEFAULT CURRENT_TIMESTAMP,
    updated_at       TIMESTAMP     DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    paid_at          TIMESTAMP     NULL,
    cancelled_at     TIMESTAMP     NULL
);

CREATE TABLE payments (
    id           INT        AUTO_INCREMENT PRIMARY KEY,
    order_id     INT        NOT NULL,
    user_id      INT        NOT NULL,
    amount_cent  BIGINT     NOT NULL,
    status       INT        NOT NULL,  -- 1->pending / 2->succeeded / 3->cancelled
    created_at   TIMESTAMP  DEFAULT CURRENT_TIMESTAMP
);

CREATE TABLE inventories (
    id          INT          AUTO_INCREMENT PRIMARY KEY,
    product_id  INT          NOT NULL UNIQUE,
    total       BIGINT       NOT NULL,
    available   BIGINT       NOT NULL,
    created_at  TIMESTAMP    DEFAULT CURRENT_TIMESTAMP,
    updated_at  TIMESTAMP    DEFAULT CURRENT_TIMESTAMP ON UPDATE CURRENT_TIMESTAMP,
    CONSTRAINT chk_available_ge_0   CHECK (available >= 0),
    CONSTRAINT chk_available_le_tot CHECK (available <= total)
);
