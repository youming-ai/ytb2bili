package types

// Task 接口定义了任务处理器的基本操作
type Task interface {
	Execute(context map[string]interface{}) bool
	GetName() string
	InsertTask() error
	UpdateStatus(status, message string) error
}
