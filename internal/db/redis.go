package db

import (
	"os"

	"github.com/redis/go-redis/v9"
)

// RDB Redis 客户端,懒连接:首个命令执行时才拨号。
// REDIS_ADDR 为空时 go-redis 默认连 localhost:6379。
var RDB *redis.Client

func initRedis() {
	RDB = redis.NewClient(&redis.Options{
		Addr:     os.Getenv("REDIS_ADDR"),
		Password: os.Getenv("REDIS_PASSWORD"),
	})
}
