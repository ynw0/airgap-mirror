package service

import (
	"fmt"
	"sync"
	"github.com/ynw0/airgap-mirror/internal/domain"
	"github.com/ynw0/airgap-mirror/internal/ports"
)

type Registry struct{mu sync.RWMutex;items map[string]ports.SourceAdapter}
func NewRegistry(adapters ...ports.SourceAdapter)(*Registry,error){r:=&Registry{items:map[string]ports.SourceAdapter{}};for _,a:=range adapters{if err:=r.Register(a);err!=nil{return nil,err}};return r,nil}
func registryKey(t domain.SourceType,p string)string{return string(t)+"\x00"+p}
func(r *Registry)Register(a ports.SourceAdapter)error{if a==nil{return fmt.Errorf("nil adapter")};k:=registryKey(a.Type(),a.Provider());r.mu.Lock();defer r.mu.Unlock();if _,ok:=r.items[k];ok{return fmt.Errorf("duplicate adapter %s/%s",a.Type(),a.Provider())};r.items[k]=a;return nil}
func(r *Registry)Get(t domain.SourceType,p string)(ports.SourceAdapter,error){r.mu.RLock();a,ok:=r.items[registryKey(t,p)];r.mu.RUnlock();if !ok{return nil,fmt.Errorf("adapter %s/%s: %w",t,p,domain.ErrNotFound)};return a,nil}
