package concurrent

import (
	"testing"

	"github.com/SurgeDM/Surge/internal/types"
)

func TestRetryStopAt_ClampedToActiveStopAt(t *testing.T) {
	task := types.Task{Offset: 0, Length: 80}
	active := &ActiveTask{Task: task}
	active.CurrentOffset.Store(20)
	active.StopAt.Store(50)

	resumeOnRetryOffset(&task, active)

	if task.Offset+task.Length > active.StopAt.Load() {
		t.Fatalf("task end=%d not clamped to StopAt=%d", task.Offset+task.Length, active.StopAt.Load())
	}
	if task.Offset != 20 {
		t.Fatalf("task.Offset=%d, want 20 (current)", task.Offset)
	}
	if task.Length != 30 {
		t.Fatalf("task.Length=%d, want 30", task.Length)
	}
	if active.Task != task {
		t.Fatal("activeTask.Task was not published")
	}
}

func TestClampWriteToStopAt(t *testing.T) {
	tests := []struct {
		name         string
		offset       int64
		newlyWritten int64
		stopAt       int64
		wantOffset   int64
		wantWritten  int64
	}{
		{name: "within bound", offset: 40, newlyWritten: 10, stopAt: 50, wantOffset: 40, wantWritten: 10},
		{name: "exact bound", offset: 50, newlyWritten: 10, stopAt: 50, wantOffset: 50, wantWritten: 10},
		{name: "overshoot reduces written", offset: 60, newlyWritten: 20, stopAt: 50, wantOffset: 50, wantWritten: 10},
		{name: "overshoot equals excess", offset: 60, newlyWritten: 10, stopAt: 50, wantOffset: 50, wantWritten: 0},
		{name: "overshoot zeros written", offset: 60, newlyWritten: 5, stopAt: 50, wantOffset: 50, wantWritten: 0},
		{name: "zero write overshoot", offset: 70, newlyWritten: 0, stopAt: 50, wantOffset: 50, wantWritten: 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gotOffset, gotWritten := clampWriteToStopAt(tt.offset, tt.newlyWritten, tt.stopAt)
			if gotOffset != tt.wantOffset || gotWritten != tt.wantWritten {
				t.Fatalf("clampWriteToStopAt(%d, %d, %d)=(%d, %d), want (%d, %d)",
					tt.offset, tt.newlyWritten, tt.stopAt, gotOffset, gotWritten, tt.wantOffset, tt.wantWritten)
			}
		})
	}
}

func TestResumeOnRetryOffset_NoProgressClampsToStopAt(t *testing.T) {
	task := types.Task{Offset: 10, Length: 80}
	active := &ActiveTask{Task: task}
	active.CurrentOffset.Store(10)
	active.StopAt.Store(40)

	resumeOnRetryOffset(&task, active)

	if task.Offset+task.Length > active.StopAt.Load() {
		t.Fatalf("task end=%d not clamped to StopAt=%d (no-progress case)",
			task.Offset+task.Length, active.StopAt.Load())
	}
	if task.Offset != 10 {
		t.Fatalf("task.Offset=%d, want 10 (no progress)", task.Offset)
	}
	if task.Length != 30 {
		t.Fatalf("task.Length=%d, want 30 (clamped even with no progress)", task.Length)
	}
}
