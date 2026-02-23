package bash

import (
	"testing"
)

func TestCategoryClassifier_Classify(t *testing.T) {
	classifier := NewCategoryClassifier(DefaultCategoryConfig())

	tests := []struct {
		command  string
		expected Category
	}{
		// Build commands
		{"go build", CategoryBuild},
		{"go build ./...", CategoryBuild},
		{"make", CategoryBuild},
		{"make all", CategoryBuild},
		{"cargo build", CategoryBuild},
		{"npm run build", CategoryBuild},
		{"task build", CategoryBuild},

		// Test commands
		{"go test", CategoryTest},
		{"go test ./...", CategoryTest},
		{"go test -v ./pkg/...", CategoryTest},
		{"pytest", CategoryTest},
		{"pytest tests/", CategoryTest},
		{"npm test", CategoryTest},
		{"npm run test", CategoryTest},
		{"make test", CategoryTest},

		// Install commands
		{"npm install", CategoryInstall},
		{"npm i express", CategoryInstall},
		{"go mod tidy", CategoryInstall},
		{"go mod download", CategoryInstall},
		{"go get github.com/pkg/errors", CategoryInstall},
		{"pip install requests", CategoryInstall},
		{"pip3 install flask", CategoryInstall},
		{"yarn install", CategoryInstall},
		{"yarn add lodash", CategoryInstall},

		// Explore commands
		{"ls", CategoryExplore},
		{"ls -la", CategoryExplore},
		{"find . -name '*.go'", CategoryExplore},
		{"grep -r 'TODO' .", CategoryExplore},
		{"cat README.md", CategoryExplore},
		{"head -n 10 file.txt", CategoryExplore},
		{"tail -f log.txt", CategoryExplore},
		{"tree", CategoryExplore},
		{"wc -l file.txt", CategoryExplore},

		// Lint commands
		{"golangci-lint run", CategoryLint},
		{"eslint src/", CategoryLint},
		{"gofmt -w .", CategoryLint},
		{"go fmt ./...", CategoryLint},
		{"prettier --write .", CategoryLint},
		{"make lint", CategoryLint},
		{"make fmt", CategoryLint},
		{"task fmt", CategoryLint},

		// Cleanup commands
		{"rm file.txt", CategoryCleanup},
		{"rm -rf node_modules", CategoryCleanup},
		{"git clean -fd", CategoryCleanup},
		{"make clean", CategoryCleanup},
		{"go clean", CategoryCleanup},
		{"docker system prune", CategoryCleanup},

		// Git commands
		{"git status", CategoryGit},
		{"git diff", CategoryGit},
		{"git log --oneline", CategoryGit},
		{"git show HEAD", CategoryGit},
		{"git branch -a", CategoryGit},
		{"git checkout main", CategoryGit},
		{"git commit -m 'msg'", CategoryGit},
		{"git push origin main", CategoryGit},
		{"git pull", CategoryGit},
		{"git fetch --all", CategoryGit},
		{"git merge feature", CategoryGit},
		{"git rebase main", CategoryGit},
		{"git stash", CategoryGit},
		{"git add .", CategoryGit},
		{"git reset --hard", CategoryGit},
		{"git clone https://github.com/example/repo", CategoryGit},
		{"git remote -v", CategoryGit},
		{"git tag v1.0.0", CategoryGit},

		// Docker commands
		{"docker run nginx", CategoryDocker},
		{"docker build -t myapp .", CategoryDocker},
		{"docker push myapp:latest", CategoryDocker},
		{"docker pull nginx:latest", CategoryDocker},
		{"docker ps -a", CategoryDocker},
		{"docker images", CategoryDocker},
		{"docker logs container-id", CategoryDocker},
		{"docker exec -it container bash", CategoryDocker},
		{"docker stop container", CategoryDocker},
		{"docker rm container", CategoryDocker},
		{"docker-compose up -d", CategoryDocker},
		{"podman run nginx", CategoryDocker},

		// Kubernetes commands
		{"kubectl get pods", CategoryK8s},
		{"kubectl apply -f deployment.yaml", CategoryK8s},
		{"kubectl describe pod mypod", CategoryK8s},
		{"helm install myrelease mychart", CategoryK8s},
		{"helm upgrade myrelease mychart", CategoryK8s},
		{"k9s", CategoryK8s},
		{"kubectx production", CategoryK8s},
		{"kubens default", CategoryK8s},
		{"kustomize build .", CategoryK8s},
		{"minikube start", CategoryK8s},
		{"kind create cluster", CategoryK8s},

		// Run commands
		{"go run main.go", CategoryRun},
		{"go run ./cmd/server", CategoryRun},
		{"python script.py", CategoryRun},
		{"python3 app.py", CategoryRun},
		{"node server.js", CategoryRun},
		{"bun run dev", CategoryRun},
		{"deno run app.ts", CategoryRun},
		{"ruby script.rb", CategoryRun},
		{"perl script.pl", CategoryRun},
		{"php index.php", CategoryRun},
		{"java -jar app.jar", CategoryRun},
		{"./script.sh", CategoryRun},
		{"./test.py", CategoryRun},
		{"bash deploy.sh", CategoryRun},

		// Plan commands
		{"task", CategoryPlan},
		{"task --list", CategoryPlan},
		{"make help", CategoryPlan},
		{"task explore", CategoryPlan},
		{"task plan", CategoryPlan},
		{"task todos", CategoryPlan},
		{"task todo", CategoryPlan},

		// New test/lint/install tools
		{"vitest run", CategoryTest},
		{"jest --coverage", CategoryTest},
		{"bun test", CategoryTest},
		{"ginkgo -r", CategoryTest},
		{"gotestsum ./...", CategoryTest},
		{"biome check .", CategoryLint},
		{"ruff check .", CategoryLint},
		{"mypy src/", CategoryLint},
		{"staticcheck ./...", CategoryLint},
		{"pnpm install", CategoryInstall},
		{"pnpm add lodash", CategoryInstall},
		{"bun install", CategoryInstall},
		{"bun add express", CategoryInstall},
		{"poetry install", CategoryInstall},
		{"poetry add requests", CategoryInstall},
		{"uv pip install flask", CategoryInstall},
		{"pdm install", CategoryInstall},
		{"pdm add numpy", CategoryInstall},

		// New explore tools
		{"fd pattern", CategoryExplore},
		{"bat file.txt", CategoryExplore},
		{"exa -la", CategoryExplore},
		{"eza --tree", CategoryExplore},
		{"jq '.key' file.json", CategoryExplore},
		{"yq '.key' file.yaml", CategoryExplore},
		{"diff file1 file2", CategoryExplore},

		// Explore commands including echo
		{"echo hello", CategoryExplore},

		// Other commands
		{"", CategoryOther},
		{"   ", CategoryOther},
	}

	for _, tt := range tests {
		t.Run(tt.command, func(t *testing.T) {
			got := classifier.Classify(tt.command)
			if got != tt.expected {
				t.Errorf("Classify(%q) = %q, want %q", tt.command, got, tt.expected)
			}
		})
	}
}

func TestDefaultCategoryConfig(t *testing.T) {
	config := DefaultCategoryConfig()
	if config == nil {
		t.Fatal("DefaultCategoryConfig() returned nil")
	}
	if len(config.Categories) == 0 {
		t.Error("DefaultCategoryConfig() returned empty categories")
	}

	expectedCategories := []Category{CategoryBuild, CategoryTest, CategoryInstall, CategoryExplore, CategoryLint, CategoryCleanup, CategoryGit, CategoryDocker, CategoryK8s, CategoryRun, CategoryPlan, CategoryEdit, CategoryClarify}
	for _, cat := range expectedCategories {
		if _, ok := config.Categories[cat]; !ok {
			t.Errorf("DefaultCategoryConfig() missing category %q", cat)
		}
	}
}

func TestClassifyTool(t *testing.T) {
	classifier := NewCategoryClassifier(DefaultCategoryConfig())

	tests := []struct {
		tool     string
		expected Category
	}{
		{"Read", CategoryExplore},
		{"Grep", CategoryExplore},
		{"Glob", CategoryExplore},
		{"LSP", CategoryExplore},
		{"Edit", CategoryEdit},
		{"Write", CategoryEdit},
		{"NotebookEdit", CategoryEdit},
		{"AskUserQuestion", CategoryClarify},
		{"ExitPlanMode", CategoryClarify},
		{"Task", CategoryPlan},
		{"TodoWrite", CategoryPlan},
		{"EnterPlanMode", CategoryPlan},
		{"Bash", CategoryOther},
		{"WebFetch", CategoryExplore},
		{"WebSearch", CategoryExplore},
		{"UnknownTool", CategoryOther},
	}

	for _, tt := range tests {
		t.Run(tt.tool, func(t *testing.T) {
			got := classifier.ClassifyTool(tt.tool)
			if got != tt.expected {
				t.Errorf("ClassifyTool(%q) = %q, want %q", tt.tool, got, tt.expected)
			}
		})
	}
}

func TestClassifyToolWithPath(t *testing.T) {
	classifier := NewCategoryClassifier(DefaultCategoryConfig())

	tests := []struct {
		tool     string
		filePath string
		expected Category
	}{
		{"Write", "/Users/moshe/.claude/plans/foo.md", CategoryPlan},
		{"Edit", "/home/user/.claude/plans/bar.md", CategoryPlan},
		{"Write", "/Users/moshe/project/main.go", CategoryEdit},
		{"Write", "", CategoryEdit},
		{"Read", "/Users/moshe/.claude/plans/foo.md", CategoryPlan},
		{"Bash", "", CategoryOther},
	}

	for _, tt := range tests {
		t.Run(tt.tool+"_"+tt.filePath, func(t *testing.T) {
			got := classifier.ClassifyToolWithPath(tt.tool, tt.filePath)
			if got != tt.expected {
				t.Errorf("ClassifyToolWithPath(%q, %q) = %q, want %q", tt.tool, tt.filePath, got, tt.expected)
			}
		})
	}
}

func TestClassifyBash(t *testing.T) {
	classifier := NewCategoryClassifier(DefaultCategoryConfig())

	tests := []struct {
		name     string
		command  string
		expected Category
	}{
		{
			name:     "comment with ls",
			command:  "# comment\nls /path",
			expected: CategoryExplore,
		},
		{
			name:     "ls and go build - build wins",
			command:  "ls /foo && go build",
			expected: CategoryBuild,
		},
		{
			name:     "go test with cd",
			command:  "cd /some/path && go test ./...",
			expected: CategoryTest,
		},
		{
			name:     "npm install with echo - install wins",
			command:  "echo 'installing' && npm install",
			expected: CategoryInstall,
		},
		{
			name:     "multiple commands - highest priority wins",
			command:  "ls && git status && go build && echo done",
			expected: CategoryBuild,
		},
		{
			name:     "edit command wins over explore",
			command:  "cat file.txt && mkdir /tmp/test",
			expected: CategoryEdit,
		},
		{
			name:     "simple ls",
			command:  "ls -la",
			expected: CategoryExplore,
		},
		{
			name:     "simple go run",
			command:  "go run main.go",
			expected: CategoryRun,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := classifier.ClassifyBash(tt.command)
			if got != tt.expected {
				t.Errorf("ClassifyBash(%q) = %q, want %q", tt.command, got, tt.expected)
			}
		})
	}
}

func TestCategoryPriority(t *testing.T) {
	tests := []struct {
		higher Category
		lower  Category
	}{
		{CategoryInstall, CategoryEdit},
		{CategoryEdit, CategoryRun},
		{CategoryRun, CategoryTest},
		{CategoryTest, CategoryBuild},
		{CategoryBuild, CategoryExplore},
		{CategoryExplore, CategoryOther},
	}

	for _, tt := range tests {
		t.Run(string(tt.higher)+" > "+string(tt.lower), func(t *testing.T) {
			if CategoryPriority(tt.higher) <= CategoryPriority(tt.lower) {
				t.Errorf("CategoryPriority(%q) = %d should be > CategoryPriority(%q) = %d",
					tt.higher, CategoryPriority(tt.higher), tt.lower, CategoryPriority(tt.lower))
			}
		})
	}
}
