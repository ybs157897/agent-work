package agentconfig

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ybs/agent-team-workbench/internal/agentwork"
	"github.com/ybs/agent-team-workbench/internal/domain"
)

const workspaceLayoutReceiptName = ".workspace-layout-migration.json"

type workspaceLayoutReceipt struct {
	Version     int                     `json:"version"`
	WorkspaceID string                  `json:"workspace_id"`
	State       string                  `json:"state"`
	Bundles     []workspaceLayoutBundle `json:"bundles"`
}

type workspaceLayoutBundle struct {
	Slug   string `json:"slug"`
	Digest string `json:"digest"`
}

// prepareWorkspaceScopeLocked is separate from single-Agent sync intents:
// its receipt freezes the complete set before any directory is relocated.
// The Importer mutex must be held and pending Agent intents reconciled first.
func (im *Importer) prepareWorkspaceScopeLocked(ctx context.Context, workspaceID string) error {
	ids, err := im.store.Workspaces().ListIDs(ctx)
	if err != nil {
		return err
	}
	return migrateWorkspaceLayout(ctx, im.dir, workspaceID, ids)
}

func migrateWorkspaceLayout(ctx context.Context, root, workspaceID string, existingIDs []string) error {
	if !ValidSlug(workspaceID) {
		return fmt.Errorf("%w: invalid workspace identity for Agent layout", domain.ErrValidation)
	}
	if err := ensureNoSymlinkUnder(root, root); err != nil {
		return err
	}
	legacy, err := LoadDir(root)
	if err != nil {
		return err
	}
	receiptPath := filepath.Join(root, workspaceLayoutReceiptName)
	if err := ensureNoSymlinkUnder(root, receiptPath); err != nil {
		return err
	}
	receipt, err := readWorkspaceLayoutReceipt(receiptPath)
	if err != nil {
		return err
	}
	if receipt != nil && receipt.State == "complete" {
		// Configurations may legitimately change after relocation. The completed
		// receipt is historical evidence, not a new authority over their content.
		if len(legacy) != 0 {
			return fmt.Errorf("%w: legacy Agent bundles reappeared after workspace migration", domain.ErrStateConflict)
		}
		return nil
	}
	if receipt == nil && len(legacy) == 0 {
		return nil
	}
	if len(existingIDs) != 1 || existingIDs[0] != workspaceID {
		return fmt.Errorf("%w: legacy Agent layout requires one unambiguous source workspace", domain.ErrStateConflict)
	}
	destination := filepath.Join(root, "workspaces", workspaceID)
	if err := ensureNoSymlinkUnder(root, destination); err != nil {
		return err
	}
	if receipt == nil {
		if len(legacy) > 1024 {
			return fmt.Errorf("%w: too many legacy Agent bundles", domain.ErrValidation)
		}
		receipt = &workspaceLayoutReceipt{Version: 1, WorkspaceID: workspaceID, State: "pending", Bundles: make([]workspaceLayoutBundle, 0, len(legacy))}
		for _, cfg := range legacy {
			if !ValidSlug(cfg.Slug) || cfg.Slug == "workspaces" {
				return fmt.Errorf("%w: reserved legacy Agent slug", domain.ErrValidation)
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			digest, err := workspaceBundleDigest(ctx, filepath.Join(root, cfg.Slug))
			if err != nil {
				return err
			}
			if _, err := os.Lstat(filepath.Join(destination, cfg.Slug)); err == nil {
				return fmt.Errorf("%w: workspace Agent bundle already exists", domain.ErrStateConflict)
			} else if !os.IsNotExist(err) {
				return err
			}
			receipt.Bundles = append(receipt.Bundles, workspaceLayoutBundle{Slug: cfg.Slug, Digest: digest})
		}
		sort.Slice(receipt.Bundles, func(i, j int) bool { return receipt.Bundles[i].Slug < receipt.Bundles[j].Slug })
		if err := writeWorkspaceLayoutReceipt(receiptPath, receipt); err != nil {
			return err
		}
	}
	if receipt.WorkspaceID != workspaceID {
		return fmt.Errorf("%w: Agent layout receipt belongs to another workspace", domain.ErrStateConflict)
	}
	expected := map[string]bool{}
	for _, bundle := range receipt.Bundles {
		expected[bundle.Slug] = true
	}
	for _, cfg := range legacy {
		if !expected[cfg.Slug] {
			return fmt.Errorf("%w: legacy Agent bundle changed during migration", domain.ErrStateConflict)
		}
	}
	if err := os.MkdirAll(destination, 0o755); err != nil {
		return err
	}
	if err := agentwork.SyncDir(filepath.Dir(destination)); err != nil {
		return err
	}
	if err := agentwork.SyncDir(root); err != nil {
		return err
	}
	for _, bundle := range receipt.Bundles {
		if err := ctx.Err(); err != nil {
			return err
		}
		src, dst := filepath.Join(root, bundle.Slug), filepath.Join(destination, bundle.Slug)
		if err := ensureNoSymlinkUnder(root, src); err != nil {
			return err
		}
		if err := ensureNoSymlinkUnder(root, dst); err != nil {
			return err
		}
		_, srcErr := os.Lstat(src)
		_, dstErr := os.Lstat(dst)
		if srcErr != nil && !os.IsNotExist(srcErr) {
			return srcErr
		}
		if dstErr != nil && !os.IsNotExist(dstErr) {
			return dstErr
		}
		if srcErr == nil && dstErr == nil {
			return fmt.Errorf("%w: Agent bundle exists at both migration locations", domain.ErrStateConflict)
		}
		if os.IsNotExist(srcErr) && os.IsNotExist(dstErr) {
			return fmt.Errorf("%w: Agent bundle missing from migration locations", domain.ErrStateConflict)
		}
		checkPath := dst
		if srcErr == nil {
			checkPath = src
		}
		digest, err := workspaceBundleDigest(ctx, checkPath)
		if err != nil {
			return err
		}
		if digest != bundle.Digest {
			return fmt.Errorf("%w: Agent bundle differs from frozen migration receipt", domain.ErrStateConflict)
		}
		if srcErr == nil {
			if err := os.Rename(src, dst); err != nil {
				return err
			}
			if err := agentwork.SyncDir(root); err != nil {
				return err
			}
			if err := agentwork.SyncDir(destination); err != nil {
				return err
			}
			verified, err := workspaceBundleDigest(ctx, dst)
			if err != nil {
				return err
			}
			if verified != bundle.Digest {
				return fmt.Errorf("%w: Agent bundle changed during relocation", domain.ErrStateConflict)
			}
		}
	}
	remaining, err := LoadDir(root)
	if err != nil {
		return err
	}
	if len(remaining) != 0 {
		return fmt.Errorf("%w: new legacy Agent bundles appeared during migration", domain.ErrStateConflict)
	}
	receipt.State = "complete"
	return writeWorkspaceLayoutReceipt(receiptPath, receipt)
}

func readWorkspaceLayoutReceipt(path string) (*workspaceLayoutReceipt, error) {
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, (1<<20)+1))
	if err != nil {
		return nil, err
	}
	if len(data) > 1<<20 {
		return nil, fmt.Errorf("%w: Agent layout receipt too large", domain.ErrStateConflict)
	}
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	var result workspaceLayoutReceipt
	if err := decoder.Decode(&result); err != nil {
		return nil, fmt.Errorf("%w: invalid Agent layout receipt", domain.ErrStateConflict)
	}
	if decoder.Decode(&struct{}{}) != io.EOF || result.Version != 1 || !ValidSlug(result.WorkspaceID) || (result.State != "pending" && result.State != "complete") || len(result.Bundles) == 0 || len(result.Bundles) > 1024 {
		return nil, fmt.Errorf("%w: invalid Agent layout receipt", domain.ErrStateConflict)
	}
	seen := map[string]bool{}
	for _, b := range result.Bundles {
		decoded, err := hex.DecodeString(b.Digest)
		if !ValidSlug(b.Slug) || b.Slug == "workspaces" || seen[b.Slug] || err != nil || len(decoded) != sha256.Size {
			return nil, fmt.Errorf("%w: invalid Agent layout bundle receipt", domain.ErrStateConflict)
		}
		seen[b.Slug] = true
	}
	return &result, nil
}

func writeWorkspaceLayoutReceipt(path string, receipt *workspaceLayoutReceipt) error {
	data, err := json.MarshalIndent(receipt, "", "  ")
	if err != nil {
		return err
	}
	return agentwork.WriteAtomicDurable(path, append(data, '\n'), 0o600)
}

func workspaceBundleDigest(ctx context.Context, root string) (string, error) {
	hash := sha256.New()
	var count int
	var total int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if entry.Type()&os.ModeSymlink != 0 {
			return fmt.Errorf("%w: symbolic link in Agent configuration bundle", domain.ErrValidation)
		}
		if entry.IsDir() {
			return nil
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() {
			return fmt.Errorf("%w: non-regular Agent configuration file", domain.ErrValidation)
		}
		count++
		total += info.Size()
		if count > 4096 || total > 64<<20 {
			return fmt.Errorf("%w: Agent configuration bundle exceeds migration bounds", domain.ErrValidation)
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		fmt.Fprintf(hash, "%s\x00%d\x00", filepath.ToSlash(rel), info.Size())
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		n, copyErr := io.Copy(hash, io.LimitReader(f, info.Size()+1))
		closeErr := f.Close()
		if copyErr != nil {
			return copyErr
		}
		if closeErr != nil {
			return closeErr
		}
		if n != info.Size() {
			return fmt.Errorf("%w: Agent bundle changed while hashing", domain.ErrStateConflict)
		}
		_, _ = io.WriteString(hash, "\x00")
		return nil
	})
	if err != nil {
		return "", err
	}
	if count == 0 {
		return "", fmt.Errorf("%w: empty Agent configuration bundle", domain.ErrValidation)
	}
	return strings.ToLower(hex.EncodeToString(hash.Sum(nil))), nil
}
