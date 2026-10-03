package main

// reasonEntry is one row of a reason catalogue: be-protocol schemas/errors-be.yaml for domain
// be, contracts/errors.yaml for this component. Only the rows this stub can raise are here;
// catalog_test.go checks them against copies of both files.
type reasonEntry struct {
	code             string
	http             int
	titleEn, titleZh string
	messageEn, msgZh string
}

var beReasons = map[string]reasonEntry{
	"INTERNAL": {"INTERNAL", 500, "Internal error", "内部错误",
		"Something went wrong. Quote the trace ID when reporting it.", "系统出错了。反馈时请提供 trace ID。"},
	"TOKEN_STALE": {"UNAUTHENTICATED", 401, "Session out of date", "登录状态已过期",
		"Your roles changed since you signed in; the session is being refreshed.", "登录之后你的角色有变化，正在刷新登录状态。"},
	"MISSING_PERMISSION": {"PERMISSION_DENIED", 403, "Not permitted", "没有权限",
		"You do not have the permission {permission}.", "你没有权限 {permission}。"},
	"NOT_FOUND": {"NOT_FOUND", 404, "Not found", "找不到",
		"The record does not exist or you cannot see it.", "记录不存在，或者你看不到它。"},
	"AUTHZ_NOT_READY": {"UNAVAILABLE", 503, "Starting up", "正在启动",
		"Permissions are still loading. Try again in a moment.", "权限数据还在加载，请稍后再试。"},
	"NOT_READY": {"UNAVAILABLE", 503, "Not ready", "尚未就绪",
		"The service is not ready yet (waiting for {waiting}).", "服务尚未就绪（在等待：{waiting}）。"},
	"TOKEN_INVALID": {"UNAUTHENTICATED", 401, "Please sign in", "请登录",
		"You are not signed in, or your session is no longer valid.", "你还没有登录，或者登录已失效。"},
	"UNSUPPORTED_DELEGATION": {"UNAUTHENTICATED", 401, "Delegation not supported", "不支持的代理方式",
		"Acting through a delegate of kind {kind} is not supported here.", "这里不支持经由 {kind} 类代理方行事。"},
	"MISSING_CALLER": {"UNAUTHENTICATED", 401, "Caller not identified", "调用方未标明",
		"A system call must name its caller in be-caller.", "系统面调用必须在 be-caller 里写明调用方。"},
	"CURSOR_INVALID": {"INVALID_ARGUMENT", 400, "List position no longer valid", "翻页位置已失效",
		"The page cursor does not match this request. Start again from the first page.", "翻页游标与本次请求不符，请从第一页重新开始。"},
	"LOCK_TIMEOUT": {"ABORTED", 409, "Busy, try again", "资源繁忙，请重试",
		"Someone else is changing the same data. Try again.", "有人正在修改同一份数据，请重试。"},
	"STATEMENT_TIMEOUT": {"DEADLINE_EXCEEDED", 504, "Took too long", "处理超时",
		"The operation took too long and was stopped.", "操作耗时过长，已被中止。"},
	"TX_CONFLICT": {"ABORTED", 409, "Conflict, try again", "并发冲突，请重试",
		"The change conflicted with another one at the same time. Try again.", "这次修改与同时发生的另一次修改冲突，请重试。"},
	"DB_POOL_EXHAUSTED": {"RESOURCE_EXHAUSTED", 429, "Too busy", "系统繁忙",
		"The service is too busy right now. Try again shortly.", "服务当前过于繁忙，请稍后再试。"},
	"DEADLINE_BUDGET_EXHAUSTED": {"DEADLINE_EXCEEDED", 504, "Took too long", "处理超时",
		"Not enough time was left to finish the operation.", "剩余时间不足以完成这次操作。"},
	"BODY_TOO_LARGE": {"INVALID_ARGUMENT", 413, "Request too large", "请求过大",
		"The request body is larger than the {limit} bytes allowed.", "请求体超过允许的 {limit} 字节。"},
	"DEPENDENCY_UNAVAILABLE": {"UNAVAILABLE", 503, "Temporarily unavailable", "暂时不可用",
		"A service this request needs ({dependency}) cannot be reached right now. Try again shortly.", "这个请求依赖的服务（{dependency}）暂时连不上，请稍后再试。"},
	"REQUEST_CANCELLED": {"CANCELLED", 499, "Request cancelled", "请求已取消",
		"The request was cancelled by the caller before it finished.", "请求在完成之前被调用方取消了。"},
	"CAPABILITY_UNAVAILABLE": {"UNIMPLEMENTED", 501, "Not available in this installation", "本系统未提供此功能",
		"The installed provider does not offer {capability}.", "当前安装的提供方不支持 {capability}。"},
	"IDEMPOTENCY_MISMATCH": {"INVALID_ARGUMENT", 400, "Request does not match its key", "请求与幂等键不符",
		"This idempotency key was already used for a different request.", "这个幂等键已经用于另一个请求。"},
	"IDEMPOTENCY_IN_PROGRESS": {"ABORTED", 409, "Still in progress", "仍在处理中",
		"The first attempt of this request has not finished yet.", "这个请求的第一次执行还没有结束。"},
	"BATCH_TOO_LARGE": {"INVALID_ARGUMENT", 400, "Batch too large", "批量过大",
		"{field} has {got} items; at most {max} are allowed.", "{field} 有 {got} 项，最多允许 {max} 项。"},
	"DB_TOO_MANY_CONNECTIONS": {"UNAVAILABLE", 503, "Service busy", "服务繁忙",
		"The database is not accepting more connections right now. Try again shortly.", "数据库暂时不接受更多连接，请稍后再试。"},
}

// ownReasons is contracts/errors.yaml (domain conformance/rawstub).
var ownReasons = map[string]reasonEntry{
	"NOTE_TITLE_REQUIRED": {"INVALID_ARGUMENT", 400, "Title required", "缺少标题",
		"A note needs a non-empty title", "便签必须有非空的标题"},
	"NOTE_ID_INVALID": {"INVALID_ARGUMENT", 400, "Invalid note ID", "便签 ID 不合法",
		"{id} is not a UUID", "{id} 不是 UUID"},
}

// grpcCodes maps canonical code names to their gRPC numbers and HTTP statuses (P4 code table).
var grpcCodes = map[string]struct{ num, http int }{
	"OK": {0, 200}, "CANCELLED": {1, 499}, "UNKNOWN": {2, 500}, "INVALID_ARGUMENT": {3, 400},
	"DEADLINE_EXCEEDED": {4, 504}, "NOT_FOUND": {5, 404}, "ALREADY_EXISTS": {6, 409},
	"PERMISSION_DENIED": {7, 403}, "RESOURCE_EXHAUSTED": {8, 429}, "FAILED_PRECONDITION": {9, 400},
	"ABORTED": {10, 409}, "OUT_OF_RANGE": {11, 400}, "UNIMPLEMENTED": {12, 501}, "INTERNAL": {13, 500},
	"UNAVAILABLE": {14, 503}, "DATA_LOSS": {15, 500}, "UNAUTHENTICATED": {16, 401},
}

// levelForCode is P4.6.
func levelForCode(code string) string {
	switch code {
	case "OK", "CANCELLED":
		return "none"
	case "INTERNAL", "UNKNOWN", "DATA_LOSS":
		return "error"
	case "UNAVAILABLE", "DEADLINE_EXCEEDED":
		return "warn"
	}
	return "info"
}

// accessLogLevel is the level of an access-log line by its code (P4.6): ERROR for INTERNAL,
// UNKNOWN, DATA_LOSS; WARN for UNAVAILABLE, DEADLINE_EXCEEDED; INFO otherwise, OK and
// CANCELLED included.
func accessLogLevel(code string) string {
	if l := levelForCode(code); l != "none" {
		return l
	}
	return "info"
}
