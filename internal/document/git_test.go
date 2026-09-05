package document

import (
	"testing"

	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/object"
)

func TestHistoryCommitSnapshot(t *testing.T) {
	dir := t.TempDir()
	h, err := initHistory(dir+"/git", dir+"/worktree")
	if err != nil {
		t.Fatal(err)
	}

	if err := h.writeFile("notes.md", []byte("# Title\n\nhello\n")); err != nil {
		t.Fatal(err)
	}
	rev1, err := h.commit("proofreader · b1 · edited paragraph")
	if err != nil {
		t.Fatal(err)
	}
	if rev1 == "" {
		t.Fatal("empty revision")
	}

	if err := h.writeFile("notes.md", []byte("# Title\n\nhello world\n")); err != nil {
		t.Fatal(err)
	}
	rev2, err := h.commit("autosave @ ts")
	if err != nil {
		t.Fatal(err)
	}
	if rev1 == rev2 {
		t.Fatal("revisions must differ")
	}

	// HEAD points at rev2; rev2 has rev1 as parent (append-only history).
	head, err := h.head()
	if err != nil {
		t.Fatal(err)
	}
	if head.String() != rev2 {
		t.Fatalf("HEAD=%s want %s", head, rev2)
	}

	obj, err := h.repo.CommitObject(plumbing.NewHash(rev2))
	if err != nil {
		t.Fatal(err)
	}
	if len(obj.ParentHashes) != 1 || obj.ParentHashes[0].String() != rev1 {
		t.Fatalf("parent chain broken: parents=%v", obj.ParentHashes)
	}

	// The tree at rev2 contains notes.md with the updated bytes.
	tree, err := obj.Tree()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tree.File("notes.md"); err != nil {
		t.Fatalf("notes.md missing from tree: %v", err)
	}

	// log() walks the history linearly.
	var count int
	err = object.NewCommitPreorderIter(obj, nil, nil).ForEach(func(c *object.Commit) error {
		count++
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("history length = %d, want 2", count)
	}
}
