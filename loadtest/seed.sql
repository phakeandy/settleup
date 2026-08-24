-- 秒杀压测种子数据:每次跑压测前先重置,保证起点一致。
-- 用法: mysql -h127.0.0.1 -P3306 -uroot -proot settleup < loadtest/seed.sql

DELETE FROM payments;
DELETE FROM orders;
DELETE FROM inventories;
DELETE FROM products;

INSERT INTO products (id, name, price_cent) VALUES (1, 'seckill-item', 9900);
INSERT INTO inventories (product_id, total, available) VALUES (1, 1000, 1000);
