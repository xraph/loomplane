package main

import (
	"context"

	"github.com/redis/go-redis/v9"
)

func main() {
	rdb := redis.NewClient(&redis.Options{Addr: "localhost:6379"})
	for {
		_, _ = rdb.BLPop(context.Background(), 0, "jobs").Result()
	}
}
