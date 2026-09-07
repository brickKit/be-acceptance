// slow-start 是平台断言 10 用的假组件：故意 45 秒冷启动才开始响应
// /healthz，用来验证"默认启动宽限是 60 秒"这条配置值真的顶得住一个
//接近但不超过它的真实冷启动耗时——不只是验配置值本身（那部分阶段一
// 已经验过：docker inspect 读 StartPeriod 是 60000000000 纳秒）。
package main

import (
	"fmt"
	"net/http"
	"time"
)

func main() {
	time.Sleep(45 * time.Second)
	http.HandleFunc("/healthz", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintln(w, "ok")
	})
	http.ListenAndServe(":19003", nil)
}
