package queue

import "sync"

// JobQueue represents a FIFO queue for file paths
type JobQueue struct {
	items chan string
	wg    sync.WaitGroup
}

// New creates a new queue with a buffer size
func New(bufferSize int) *JobQueue {
	return &JobQueue{
		items: make(chan string, bufferSize),
	}
}

// Push adds a file to the queue
func (q *JobQueue) Push(path string) {
	q.items <- path
}

// Process starts a worker that processes items using the handler.
// It blocks until the channel is closed.
func (q *JobQueue) Process(handler func(string)) {
	for path := range q.items {
		handler(path)
	}
}

// Close closes the queue channel
func (q *JobQueue) Close() {
	close(q.items)
}
