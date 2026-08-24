package engine

import "testing"

func TestParseSSEComplete(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantNil bool
		created int
		syncFail int
	}{
		{
			name: "正常导入 created=1 syncFailed=0",
			body: `: connected

event: progress
data: {"completed":1,"total":1,"phase":"importing"}

event: progress
data: {"completed":1,"total":1,"phase":"syncing"}

event: complete
data: {"created":1,"updated":0,"skipped":0,"failed":0,"synced":1,"syncFailed":0}`,
			wantNil: false,
			created: 1,
			syncFail: 0,
		},
		{
			name: "同步失败(无效 token)",
			body: `event: progress
data: {"completed":1,"total":1,"phase":"syncing"}

event: complete
data: {"created":1,"updated":0,"skipped":0,"failed":0,"synced":0,"syncFailed":1}`,
			wantNil: false,
			created: 1,
			syncFail: 1,
		},
		{
			name: "error 事件",
			body: `event: error
data: {"message":"导入失败"}`,
			wantNil: true,
		},
		{
			name: "缺少 complete 事件",
			body: `event: progress
data: {"completed":0,"total":1}`,
			wantNil: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := parseSSEComplete(tt.body)
			if tt.wantNil {
				if result != nil {
					t.Errorf("期望 nil,实际 %+v", result)
				}
				return
			}
			if result == nil {
				t.Fatalf("期望非 nil,实际 nil")
			}
			if result.Created != tt.created {
				t.Errorf("created = %d, 期望 %d", result.Created, tt.created)
			}
			if result.SyncFailed != tt.syncFail {
				t.Errorf("syncFailed = %d, 期望 %d", result.SyncFailed, tt.syncFail)
			}
		})
	}
}

func TestTruncateForLog(t *testing.T) {
	if got := truncateForLog("short", 10); got != "short" {
		t.Errorf("短字符串应原样返回,得到 %q", got)
	}
	if got := truncateForLog("0123456789abcdef", 10); got != "0123456789..." {
		t.Errorf("长字符串应截断,得到 %q", got)
	}
}
