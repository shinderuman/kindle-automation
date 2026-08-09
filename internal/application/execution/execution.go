// Package execution は worker ユースケースの1ジョブ処理結果を表す結果DTOを提供する。
//
// 各ユースケースハンドラが Outcome を生成し、composition root（internal/lambda/checkworker）が
// job_id/cycle_id/target/receive_count/aws_request_id/duration を合成して SPECIFICATION.md 18.1 の
// 固定共通field形式で構造化ログへ出す。正常・terminal・retryable で同じ field 形式を保証するため、
// ログの組み立ては composition root のみが行う。
//
// 本packageは domain/HTTP/AWS/logging のいずれへも依存しない（AGENTS.md §2/§3）。
package execution

// 結果分類（SPECIFICATION.md 18.1 result）。正常・terminal・retryable で共通に使う。
const (
	// ResultCompleted は正常処理結果（job_completed）。
	ResultCompleted = "completed"
	// ResultTerminal は404・対象種別不一致等の再試行しない結果（job_terminal）。
	ResultTerminal = "terminal"
	// ResultError は再試行する処理エラー（job_error / gist_error）。
	ResultError = "error"
)

// Outcome は1ジョブの処理結果。
// HTTPStatus/ResponseBytes は Amazon 取得1回の計測値で、未送信時（Amazon 未アクセスの job や
// fetch 前の失敗）は0。composition root は未取得値を string 空・数値0 で常に出力する。
type Outcome struct {
	Result        string
	ErrorType     string
	HTTPStatus    int
	ResponseBytes int
}

// Completed は正常結果の Outcome を返す。
func Completed(httpStatus, responseBytes int) Outcome {
	return Outcome{Result: ResultCompleted, HTTPStatus: httpStatus, ResponseBytes: responseBytes}
}

// Terminal は再試行しない結果の Outcome を返す。errorType に具体原因を設定する。
func Terminal(errorType string, httpStatus, responseBytes int) Outcome {
	return Outcome{Result: ResultTerminal, ErrorType: errorType, HTTPStatus: httpStatus, ResponseBytes: responseBytes}
}

// Errored は再試行する処理エラーの Outcome を返す。原因 error は呼び出し側が戻り値として保持する。
func Errored(errorType string, httpStatus, responseBytes int) Outcome {
	return Outcome{Result: ResultError, ErrorType: errorType, HTTPStatus: httpStatus, ResponseBytes: responseBytes}
}
