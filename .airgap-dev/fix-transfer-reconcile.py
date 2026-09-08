from pathlib import Path

bundle = Path("internal/client/bundle_transfer.go")
s = bundle.read_text(encoding="utf-8")
old = '''type BundleTransferResult struct {
\tDescriptor domain.BatchDescriptor `json:"descriptor"`
\tStatus     ImportStatus           `json:"status"`
}'''
new = '''type BundleTransferResult struct {
\tDescriptor     domain.BatchDescriptor `json:"descriptor"`
\tStatus         ImportStatus           `json:"status"`
\tAlreadyApplied bool                   `json:"alreadyApplied"`
}'''
if old not in s:
    raise SystemExit("BundleTransferResult source shape changed")
s = s.replace(old, new, 1)
old = '''\tif !state.LiveCursor.Equal(d.BaseCursor) {
\t\treturn out, fmt.Errorf("server cursor %v differs from bundle base %v: %w", state.LiveCursor, d.BaseCursor, domain.ErrConflict)
\t}
\tif state.ActiveEpochID != "" && state.ActiveEpochID != d.EpochID {
\t\treturn out, fmt.Errorf("server source has active epoch %s: %w", state.ActiveEpochID, domain.ErrConflict)
\t}'''
new = '''\tif !state.LiveCursor.Equal(d.BaseCursor) {
\t\tif state.LiveCursor.Equal(d.TargetCursor) && state.ActiveEpochID == "" {
\t\t\tserverEpoch, epochErr := agent.Epoch(ctx, d.EpochID)
\t\t\tif epochErr != nil {
\t\t\t\treturn out, epochErr
\t\t\t}
\t\t\tif serverEpoch.ID != d.EpochID || serverEpoch.SourceID != d.SourceID || !serverEpoch.BaseCursor.Equal(d.BaseCursor) || !serverEpoch.TargetCursor.Equal(d.TargetCursor) || serverEpoch.TotalBytes != d.EpochTotalBytes || serverEpoch.TotalObjects != d.EpochTotalObjects || serverEpoch.TotalBatches != d.EpochTotalBatches || serverEpoch.PublishUnitCount != d.EpochPublishUnitCount || serverEpoch.Status != domain.EpochComplete {
\t\t\t\treturn out, fmt.Errorf("server cursor reached target but epoch %s does not match completed bundle epoch: %w", d.EpochID, domain.ErrConflict)
\t\t\t}
\t\t\tout.AlreadyApplied = true
\t\t\treturn out, nil
\t\t}
\t\treturn out, fmt.Errorf("server cursor %v differs from bundle base %v: %w", state.LiveCursor, d.BaseCursor, domain.ErrConflict)
\t}
\tif state.ActiveEpochID != "" && state.ActiveEpochID != d.EpochID {
\t\treturn out, fmt.Errorf("server source has active epoch %s: %w", state.ActiveEpochID, domain.ErrConflict)
\t}'''
if old not in s:
    raise SystemExit("bundle cursor preflight source shape changed")
s = s.replace(old, new, 1)
bundle.write_text(s, encoding="utf-8")

workspace = Path("internal/client/workspace_transfer.go")
s = workspace.read_text(encoding="utf-8")
old = '''\tfor _, next := range []domain.EpochStatus{domain.EpochPublishable, domain.EpochPublished, domain.EpochComplete} {
\t\tif epoch.Status == next || epoch.Status == domain.EpochComplete {
\t\t\tcontinue
\t\t}
\t\tif err = domain.ValidateEpochTransition(epoch.Status, next); err != nil {
\t\t\treturn epoch, err
\t\t}
\t\tepoch.Status = next
\t\tif err = w.Store.UpdateEpoch(ctx, epoch); err != nil {
\t\t\treturn epoch, err
\t\t}
\t}
\treturn epoch, nil'''
new = '''\tfor epoch.Status != domain.EpochComplete {
\t\tvar next domain.EpochStatus
\t\tswitch epoch.Status {
\t\tcase domain.EpochTransferring:
\t\t\tnext = domain.EpochPublishable
\t\tcase domain.EpochPublishable:
\t\t\tnext = domain.EpochPublished
\t\tcase domain.EpochPublished:
\t\t\tnext = domain.EpochComplete
\t\tdefault:
\t\t\treturn epoch, fmt.Errorf("local epoch %s cannot reconcile completion from %s: %w", epoch.ID, epoch.Status, domain.ErrConflict)
\t\t}
\t\tif err = domain.ValidateEpochTransition(epoch.Status, next); err != nil {
\t\t\treturn epoch, err
\t\t}
\t\tepoch.Status = next
\t\tif err = w.Store.UpdateEpoch(ctx, epoch); err != nil {
\t\t\treturn epoch, err
\t\t}
\t}
\treturn epoch, nil'''
if old not in s:
    raise SystemExit("epoch completion loop source shape changed")
s = s.replace(old, new, 1)
old = '''\tdescriptor, err := ReadBatchDescriptor(bundleDir)
\tif err != nil {
\t\treturn out, err
\t}
\tbatch, err := w.Store.GetBatch(ctx, descriptor.BatchID)'''
new = '''\tvalidated, err := ValidateBatchBundle(ctx, bundleDir)
\tif err != nil {
\t\treturn out, err
\t}
\tdescriptor := validated.Descriptor
\tbatch, err := w.Store.GetBatch(ctx, descriptor.BatchID)'''
if old not in s:
    raise SystemExit("workspace bundle validation source shape changed")
s = s.replace(old, new, 1)
workspace.write_text(s, encoding="utf-8")
