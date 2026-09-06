package document

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/go-git/go-git/v5"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/filemode"
	"github.com/go-git/go-git/v5/plumbing/object"
)

// historyStore owns the engine's git history (ADR-0004 §2, ADR-0020 §2): a
// bare-enough repo holding append-only commit history, plus an engine-owned
// working-tree directory holding the current canonical markdown. The engine
// writes the working tree on every accepted/manual edit and commits a tree
// snapshot per commit (one accepted edit == one commit; autosave == one snapshot).
type historyStore struct {
	repo        *git.Repository
	worktreeDir string
}

// initHistory opens (or creates) the git history repo and its working tree.
func initHistory(gitDir, worktreeDir string) (*historyStore, error) {
	repo, err := git.PlainOpen(gitDir)
	if err == git.ErrRepositoryNotExists {
		repo, err = git.PlainInit(gitDir, true) // bare
	}
	if err != nil {
		return nil, fmt.Errorf("open git repo: %w", err)
	}
	if err := os.MkdirAll(worktreeDir, 0o755); err != nil {
		return nil, fmt.Errorf("create worktree: %w", err)
	}
	return &historyStore{repo: repo, worktreeDir: worktreeDir}, nil
}

// writeFile writes canonical markdown into the working tree. It is the single
// path by which document bytes reach disk (canonical-content invariant, ADR-0029).
func (h *historyStore) writeFile(name string, data []byte) error {
	path := filepath.Join(h.worktreeDir, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

// readFile returns the current working-tree bytes for a document file.
func (h *historyStore) readFile(name string) (string, error) {
	path := filepath.Join(h.worktreeDir, filepath.FromSlash(name))
	b, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	return string(b), nil
}

// fileAt returns a file's content at a specific commit hash.
func (h *historyStore) fileAt(name string, rev string) (string, error) {
	hash := plumbing.NewHash(rev)
	commit, err := h.repo.CommitObject(hash)
	if err != nil {
		return "", err
	}
	tree, err := commit.Tree()
	if err != nil {
		return "", err
	}
	f, err := tree.File(name)
	if err != nil {
		return "", err
	}
	r, err := f.Reader()
	if err != nil {
		return "", err
	}
	defer r.Close()
	b, err := io.ReadAll(r)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

// commitInfo pairs a commit hash with its metadata for History/headRev reads.
type commitInfo struct {
	hash string
	msg  string
	ts   int64
}

// log walks history newest-first along the HEAD commit chain.
func (h *historyStore) log() ([]commitInfo, error) {
	head, err := h.repo.Head()
	if err != nil {
		if err == plumbing.ErrReferenceNotFound {
			return nil, nil // no commits yet
		}
		return nil, err
	}
	iter, err := h.repo.Log(&git.LogOptions{From: head.Hash()})
	if err != nil {
		return nil, err
	}
	defer iter.Close()
	var out []commitInfo
	err = iter.ForEach(func(c *object.Commit) error {
		out = append(out, commitInfo{
			hash: c.Hash.String(),
			msg:  strings.TrimSpace(c.Message),
			ts:   c.Committer.When.Unix(),
		})
		return nil
	})
	if err != nil {
		return nil, err
	}
	return out, nil
}

// commit snapshots the working tree into the history as one commit.
func (h *historyStore) commit(msg string) (string, error) {
	treeHash, err := h.buildTree()
	if err != nil {
		return "", err
	}

	commit := &object.Commit{
		Author:    signature(),
		Committer: signature(),
		Message:   msg,
		TreeHash:  treeHash,
	}
	if head, err := h.repo.Head(); err == nil {
		commit.ParentHashes = []plumbing.Hash{head.Hash()}
	}

	obj := h.repo.Storer.NewEncodedObject()
	if err := commit.Encode(obj); err != nil {
		return "", err
	}
	hash, err := h.repo.Storer.SetEncodedObject(obj)
	if err != nil {
		return "", err
	}
	ref := plumbing.NewHashReference(plumbing.HEAD, hash)
	if err := h.repo.Storer.SetReference(ref); err != nil {
		return "", err
	}
	return hash.String(), nil
}

func (h *historyStore) head() (plumbing.Hash, error) {
	ref, err := h.repo.Head()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	return ref.Hash(), nil
}

func (h *historyStore) buildTree() (plumbing.Hash, error) {
	tree, err := h.buildTreeAt("")
	if err != nil {
		return plumbing.ZeroHash, err
	}
	return h.storeTree(tree)
}

func (h *historyStore) buildTreeAt(rel string) (*object.Tree, error) {
	dir := filepath.Join(h.worktreeDir, filepath.FromSlash(rel))
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	tree := &object.Tree{}
	for _, e := range entries {
		name := e.Name()
		full := filepath.Join(dir, name)
		childRel := name
		if rel != "" {
			childRel = rel + "/" + name
		}
		if e.IsDir() {
			childTree, err := h.buildTreeAt(childRel)
			if err != nil {
				return nil, err
			}
			childHash, err := h.storeTree(childTree)
			if err != nil {
				return nil, err
			}
			tree.Entries = append(tree.Entries, object.TreeEntry{
				Name: name, Mode: filemode.Dir, Hash: childHash,
			})
			continue
		}
		info, err := e.Info()
		if err != nil {
			return nil, err
		}
		if !info.Mode().IsRegular() {
			continue
		}
		blobHash, err := h.storeBlob(full)
		if err != nil {
			return nil, err
		}
		mode := filemode.Regular
		if info.Mode().Perm()&0o111 != 0 {
			mode = filemode.Executable
		}
		tree.Entries = append(tree.Entries, object.TreeEntry{
			Name: name, Mode: mode, Hash: blobHash,
		})
	}
	sort.Sort(byTreeEntryName(tree.Entries))
	return tree, nil
}

type byTreeEntryName []object.TreeEntry

func (s byTreeEntryName) Len() int           { return len(s) }
func (s byTreeEntryName) Less(i, j int) bool { return s[i].Name < s[j].Name }
func (s byTreeEntryName) Swap(i, j int)      { s[i], s[j] = s[j], s[i] }

func (h *historyStore) storeTree(tree *object.Tree) (plumbing.Hash, error) {
	obj := h.repo.Storer.NewEncodedObject()
	if err := tree.Encode(obj); err != nil {
		return plumbing.ZeroHash, err
	}
	return h.repo.Storer.SetEncodedObject(obj)
}

func (h *historyStore) storeBlob(path string) (plumbing.Hash, error) {
	f, err := os.Open(path)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	defer f.Close()
	obj := h.repo.Storer.NewEncodedObject()
	obj.SetType(plumbing.BlobObject)
	info, err := f.Stat()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	obj.SetSize(info.Size())
	w, err := obj.Writer()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	if _, err := io.Copy(w, f); err != nil {
		return plumbing.ZeroHash, err
	}
	return h.repo.Storer.SetEncodedObject(obj)
}

func signature() object.Signature {
	return object.Signature{
		Name:  "writing-assistant",
		Email: "engine@localhost",
		When:  time.Now().UTC(),
	}
}
