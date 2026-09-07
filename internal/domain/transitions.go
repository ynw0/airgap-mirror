package domain

import "fmt"

func CanTransitionEpoch(from,to EpochStatus) bool { if from==to{return true}; if to==EpochFailed||to==EpochCancelled{return from!=EpochComplete&&from!=EpochCancelled}; allowed:=map[EpochStatus]EpochStatus{EpochPlanned:EpochDownloading,EpochDownloading:EpochTransferring,EpochTransferring:EpochPublishable,EpochPublishable:EpochPublished,EpochPublished:EpochComplete}; return allowed[from]==to }
func ValidateEpochTransition(from,to EpochStatus) error { if !CanTransitionEpoch(from,to){return fmt.Errorf("invalid epoch transition %s -> %s",from,to)}; return nil }
func CanTransitionBatch(from,to BatchStatus) bool { if from==to{return true}; if to==BatchFailed{return from!=BatchImported}; allowed:=map[BatchStatus]BatchStatus{BatchPlanned:BatchDownloading,BatchDownloading:BatchVerifying,BatchVerifying:BatchReady,BatchReady:BatchTransferring,BatchTransferring:BatchImporting,BatchImporting:BatchImported}; return allowed[from]==to }
func ValidateBatchTransition(from,to BatchStatus) error { if !CanTransitionBatch(from,to){return fmt.Errorf("invalid batch transition %s -> %s",from,to)}; return nil }
func CanTransitionPack(from,to PackStatus) bool { if from==to{return true}; if to==PackFailed{return from!=PackImported}; allowed:=map[PackStatus]PackStatus{PackPlanned:PackWriting,PackWriting:PackReady,PackReady:PackUploading,PackUploading:PackUploaded,PackUploaded:PackImporting,PackImporting:PackImported}; return allowed[from]==to }
func ValidatePackTransition(from,to PackStatus) error { if !CanTransitionPack(from,to){return fmt.Errorf("invalid pack transition %s -> %s",from,to)}; return nil }
