package marketplace

import (
	"fmt"
	"sync"
	"time"
)

const jobTTL = 30 * time.Minute

// InstallJobManager tracks installation jobs and their progress
type InstallJobManager struct {
	jobs map[string]*InstallJob
	mu   sync.RWMutex
}

// InstallJob represents a single installation task with progress and logs
type InstallJob struct {
	ID         string    `json:"id"`
	Status     string    `json:"status"`   // "running", "completed", "failed"
	Progress   int       `json:"progress"` // 0-100
	Logs       []string  `json:"logs"`
	Error      string    `json:"error,omitempty"`
	finishedAt time.Time // set when status becomes completed or failed
	mu         sync.RWMutex
}

// NewInstallJobManager creates a new job manager
func NewInstallJobManager() *InstallJobManager {
	return &InstallJobManager{
		jobs: make(map[string]*InstallJob),
	}
}

// NewInstallJob creates and registers a new installation job.
// It also purges finished jobs older than jobTTL.
func (m *InstallJobManager) NewInstallJob(id string) *InstallJob {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	for k, j := range m.jobs {
		j.mu.RLock()
		finished := !j.finishedAt.IsZero() && now.Sub(j.finishedAt) > jobTTL
		j.mu.RUnlock()
		if finished {
			delete(m.jobs, k)
		}
	}

	job := &InstallJob{
		ID:     id,
		Status: "running",
		Logs:   []string{},
	}
	m.jobs[id] = job
	return job
}

// GetJob retrieves a job by ID
func (m *InstallJobManager) GetJob(id string) (*InstallJob, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	job, ok := m.jobs[id]
	return job, ok
}

// AddLog appends a timestamped log message
func (j *InstallJob) AddLog(message string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Logs = append(j.Logs, fmt.Sprintf("[%s] %s", time.Now().Format("15:04:05"), message))
}

// SetProgress updates the progress percentage (clamped 0-100)
func (j *InstallJob) SetProgress(percent int) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if percent < 0 {
		percent = 0
	}
	if percent > 100 {
		percent = 100
	}
	j.Progress = percent
}

// SetStatus updates the job status
func (j *InstallJob) SetStatus(status string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Status = status
	if status == "completed" || status == "failed" {
		j.finishedAt = time.Now()
	}
}

// SetError marks the job as failed with an error message
func (j *InstallJob) SetError(err string) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.Error = err
	j.Status = "failed"
	j.finishedAt = time.Now()
}

// JobSnapshot is a point-in-time copy of a job's state, safe to read without locks
type JobSnapshot struct {
	ID       string   `json:"id"`
	Status   string   `json:"status"`
	Progress int      `json:"progress"`
	Logs     []string `json:"logs"`
	Error    string   `json:"error,omitempty"`
}

// GetSnapshot returns a thread-safe snapshot of the current job state
func (j *InstallJob) GetSnapshot() *JobSnapshot {
	j.mu.RLock()
	defer j.mu.RUnlock()
	return &JobSnapshot{
		ID:       j.ID,
		Status:   j.Status,
		Progress: j.Progress,
		Logs:     append([]string{}, j.Logs...),
		Error:    j.Error,
	}
}
