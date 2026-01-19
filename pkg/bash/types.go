package bash

type OperationType string

const (
	OpCreate OperationType = "create"
	OpModify OperationType = "modify"
	OpDelete OperationType = "delete"
)

type FileOperation struct {
	Path      string
	Operation OperationType
	Command   string
	Line      int
	HasGlob   bool
	HasVar    bool
}

type AnalysisResult struct {
	Operations []FileOperation
}

func (r *AnalysisResult) Created() []FileOperation {
	return r.filterByOp(OpCreate)
}

func (r *AnalysisResult) Modified() []FileOperation {
	return r.filterByOp(OpModify)
}

func (r *AnalysisResult) Deleted() []FileOperation {
	return r.filterByOp(OpDelete)
}

func (r *AnalysisResult) Paths() []string {
	seen := make(map[string]bool)
	var paths []string
	for _, op := range r.Operations {
		if !seen[op.Path] {
			seen[op.Path] = true
			paths = append(paths, op.Path)
		}
	}
	return paths
}

func (r *AnalysisResult) filterByOp(op OperationType) []FileOperation {
	var result []FileOperation
	for _, o := range r.Operations {
		if o.Operation == op {
			result = append(result, o)
		}
	}
	return result
}
