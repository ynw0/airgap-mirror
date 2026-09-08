package service

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/ports"
)

type PublisherService struct{Store ports.ServerStore;Catalog ports.CatalogStore;Registry ports.AdapterRegistry;Now func()time.Time}
func(p PublisherService)now()time.Time{if p.Now!=nil{return p.Now()};return time.Now()}
func publishPriority(t domain.SourceType,path string)int{if t!=domain.SourceAPT{return 10};base:=filepath.Base(path);switch base{case "Release":return 90;case "Release.gpg":return 95;case "InRelease":return 100;default:if strings.Contains(path,"/by-hash/"){return 20};return 10}}
func(p PublisherService)PublishReady(ctx context.Context,epochID string)error{ep,err:=p.Store.GetEpoch(ctx,epochID);if err!=nil{return err};source,err:=p.Store.GetSource(ctx,ep.SourceID);if err!=nil{return err};units,err:=p.Store.ListPublishUnits(ctx,epochID);if err!=nil{return err};for _,u:=range units{if u.Status==domain.PublishPublished{continue};if u.Imported!=u.Required{continue};if u.Status!=domain.PublishReady&&u.Status!=domain.PublishWaiting{return fmt.Errorf("publish unit %s status %s: %w",u.ID,u.Status,domain.ErrConflict)};if p.Registry!=nil{a,e:=p.Registry.Get(source.Type,source.Provider);if e!=nil{return e};if e=a.ValidatePublish(ctx,ports.PublishValidationRequest{Source:source,Epoch:ep,Unit:u,Catalog:p.Catalog});e!=nil{return fmt.Errorf("validate publish unit %s: %w",u.Key,e)}};entries,e:=p.Store.ListPublishMetadata(ctx,u.ID);if e!=nil{return e};sort.Slice(entries,func(i,j int)bool{pi,pj:=publishPriority(source.Type,entries[i].LogicalPath),publishPriority(source.Type,entries[j].LogicalPath);if pi!=pj{return pi<pj};return entries[i].LogicalPath<entries[j].LogicalPath});for _,m:=range entries{if e=p.publishMetadata(ctx,source,m);e!=nil{return fmt.Errorf("publish %s: %w",m.LogicalPath,e)}};if e=p.Store.MarkPublished(ctx,u.ID,p.now());e!=nil{return e}};return nil}
func(p PublisherService)publishMetadata(ctx context.Context,source domain.Source,m domain.PublishMetadataEntry)error{target,err:=safeTarget(source.RootPath,m.LogicalPath);if err!=nil{return err};if err=rejectSymlinkParents(source.RootPath,target);err!=nil{return err};stagedHash,stagedSize,stageErr:=hashFile(m.StagedPath);if stageErr==nil{if stagedHash!=m.SHA256||stagedSize!=m.Size{return fmt.Errorf("staged metadata integrity mismatch: %w",domain.ErrConflict)};if err=os.MkdirAll(filepath.Dir(target),0755);err!=nil{return err};if err=rejectSymlinkParents(source.RootPath,target);err!=nil{return err};if err=os.Rename(m.StagedPath,target);err!=nil{return err};if d,e:=os.Open(filepath.Dir(target));e==nil{_ = d.Sync();_ = d.Close()}}else if errors.Is(stageErr,os.ErrNotExist){h,n,e:=hashFile(target);if e!=nil{return fmt.Errorf("staged metadata missing and target unavailable: %w",stageErr)};if h!=m.SHA256||n!=m.Size{return fmt.Errorf("target metadata differs after interrupted publish: %w",domain.ErrConflict)}}else{return stageErr};return p.Catalog.Upsert(ctx,domain.CatalogEntry{SourceID:m.SourceID,LogicalPath:m.LogicalPath,Size:m.Size,SHA256:m.SHA256,PackageKey:m.PackageKey,Version:m.Version,Attributes:m.Attributes,UpdatedAt:p.now()})}
