package filehistory

import "time"

// realNow 是生产时钟（毫秒，对账 TS 的 `Date.now()`）。
func realNow() int64 { return time.Now().UnixMilli() }
