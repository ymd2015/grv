package main

import (
	"bytes"
	"errors"
	"fmt"
	"reflect"
	"sync"

	log "github.com/Sirupsen/logrus"
	slice "github.com/bradfitz/slice"
	git "gopkg.in/libgit2/git2go.v25"
)

const (
	rdlCommitBufferSize = 100
	rdlDiffStatsCols    = 80
	rdlShortOidLen      = 7
)

type instanceCache struct {
	oids       map[string]*Oid
	commits    map[string]*Commit
	oidLock    sync.Mutex
	commitLock sync.Mutex
}

// RepoDataLoader handles loading data from the repository
type RepoDataLoader struct {
	repo     *git.Repository
	cache    *instanceCache
	channels *Channels
}

// Oid is reference to a git object
type Oid struct {
	oid *git.Oid
}

// Branch contains data for a branch reference
type Branch struct {
	oid      *Oid
	name     string
	isRemote bool
}

// Tag contains data for a tag reference
type Tag struct {
	oid  *Oid
	name string
	tag  *git.Tag
}

// Commit contains data for a commit
type Commit struct {
	oid    *Oid
	commit *git.Commit
}

// Diff contains data for a generated diff
type Diff struct {
	diffText bytes.Buffer
	stats    bytes.Buffer
}

// StatusEntryType describes the type of change a status entry has undergone
type StatusEntryType int

// The set of supported StatusEntryTypes
const (
	SetNew StatusEntryType = iota
	SetModified
	SetDeleted
	SetRenamed
	SetTypeChange
	SetConflicted
)

var statusEntryTypeMap = map[git.Status]StatusEntryType{
	git.StatusIndexNew:        SetNew,
	git.StatusIndexModified:   SetModified,
	git.StatusIndexDeleted:    SetDeleted,
	git.StatusIndexRenamed:    SetRenamed,
	git.StatusIndexTypeChange: SetTypeChange,
	git.StatusWtNew:           SetNew,
	git.StatusWtModified:      SetModified,
	git.StatusWtDeleted:       SetDeleted,
	git.StatusWtTypeChange:    SetTypeChange,
	git.StatusWtRenamed:       SetRenamed,
	git.StatusConflicted:      SetConflicted,
}

// StatusEntry contains data for a single status entry
type StatusEntry struct {
	statusEntryType StatusEntryType
	diffDelta       git.DiffDelta
	rawStatusEntry  git.StatusEntry
}

func newStatusEntry(gitStatus git.Status, statusType StatusType, rawStatusEntry git.StatusEntry) *StatusEntry {
	var diffDelta git.DiffDelta

	if statusType == StStaged {
		diffDelta = rawStatusEntry.HeadToIndex
	} else {
		diffDelta = rawStatusEntry.IndexToWorkdir
	}

	return &StatusEntry{
		statusEntryType: statusEntryTypeMap[gitStatus],
		diffDelta:       diffDelta,
		rawStatusEntry:  rawStatusEntry,
	}
}

// StatusType describes the different stages a status entry can be in
type StatusType int

// The different status stages
const (
	StStaged StatusType = iota
	StUnstaged
	StUntracked
	StConflicted
)

var statusTypeMap = map[git.Status]StatusType{
	git.StatusIndexNew | git.StatusIndexModified | git.StatusIndexDeleted | git.StatusIndexRenamed | git.StatusIndexTypeChange: StStaged,
	git.StatusWtModified | git.StatusWtDeleted | git.StatusWtTypeChange | git.StatusWtRenamed:                                  StUnstaged,
	git.StatusWtNew:      StUntracked,
	git.StatusConflicted: StConflicted,
}

// Status contains all git status data
type Status struct {
	entries map[StatusType][]*StatusEntry
}

func newStatus() *Status {
	return &Status{
		entries: make(map[StatusType][]*StatusEntry),
	}
}

// StatusTypes returns the current status stages which have entries
func (status *Status) StatusTypes() (statusTypes []StatusType) {
	for statusType := range status.entries {
		statusTypes = append(statusTypes, statusType)
	}

	slice.Sort(statusTypes, func(i, j int) bool {
		return statusTypes[i] < statusTypes[j]
	})

	return
}

// Entries returns the status entries for the provided status type
func (status *Status) Entries(statusType StatusType) []*StatusEntry {
	statusEntries, ok := status.entries[statusType]
	if !ok {
		return nil
	}

	return statusEntries
}

// IsEmpty returns true if there are no entries
func (status *Status) IsEmpty() bool {
	entryNum := 0

	for _, statusEntries := range status.entries {
		entryNum += len(statusEntries)
	}

	return entryNum == 0
}

func (status *Status) addEntry(rawStatusEntry git.StatusEntry) {
	for rawStatus, statusType := range statusTypeMap {
		processedRawStatus := rawStatusEntry.Status & rawStatus

		if processedRawStatus > 0 {
			if _, ok := status.entries[statusType]; !ok {
				statusEntries := make([]*StatusEntry, 0)
				status.entries[statusType] = statusEntries
			}

			status.entries[statusType] = append(status.entries[statusType],
				newStatusEntry(processedRawStatus, statusType, rawStatusEntry))
		}
	}
}

// Equal returns true if both status' contain the same files in the same stages
func (status *Status) Equal(other *Status) bool {
	statusTypes := status.StatusTypes()
	otherStatusTypes := other.StatusTypes()

	if !reflect.DeepEqual(statusTypes, otherStatusTypes) {
		return false
	}

	for _, statusType := range statusTypes {
		if !statusEntriesEqual(status.Entries(statusType), other.Entries(statusType)) {
			return false
		}
	}

	return true
}

func statusEntriesEqual(entries, otherEntries []*StatusEntry) bool {
	if len(entries) != len(otherEntries) {
		return false
	}

	// Simply check if the same set of files have been modified in the same way
	for entryIndex, entry := range entries {
		otherEntry := otherEntries[entryIndex]

		if entry.diffDelta.Status != otherEntry.diffDelta.Status ||
			entry.diffDelta.NewFile.Path != otherEntry.diffDelta.NewFile.Path {
			return false
		}
	}

	return true
}

// String returns the oid hash
func (oid Oid) String() string {
	return oid.oid.String()
}

// ShortID returns a shortened oid hash
func (oid Oid) ShortID() (shortID string) {
	id := oid.String()

	if len(id) >= rdlShortOidLen {
		shortID = id[0:rdlShortOidLen]
	}

	return
}

// String returns branch data in a string format
func (branch Branch) String() string {
	return fmt.Sprintf("%v:%v", branch.name, branch.oid)
}

// Tag returns tag data in a string format
func (tag Tag) String() string {
	return fmt.Sprintf("%v:%v", tag.name, tag.oid)
}

func newInstanceCache() *instanceCache {
	return &instanceCache{
		oids:    make(map[string]*Oid),
		commits: make(map[string]*Commit),
	}
}

func (cache *instanceCache) getOid(rawOid *git.Oid) *Oid {
	cache.oidLock.Lock()
	defer cache.oidLock.Unlock()

	oidStr := rawOid.String()

	if oid, ok := cache.oids[oidStr]; ok {
		return oid
	}

	oid := &Oid{oid: rawOid}
	cache.oids[oidStr] = oid

	return oid
}

func (cache *instanceCache) getCommit(rawCommit *git.Commit) *Commit {
	cache.commitLock.Lock()
	defer cache.commitLock.Unlock()

	oidStr := rawCommit.Id().String()

	if commit, ok := cache.commits[oidStr]; ok {
		return commit
	}

	commit := &Commit{
		oid:    cache.getOid(rawCommit.Id()),
		commit: rawCommit,
	}
	cache.commits[oidStr] = commit

	return commit
}

// NewRepoDataLoader creates a new instance
func NewRepoDataLoader(channels *Channels) *RepoDataLoader {
	return &RepoDataLoader{
		cache:    newInstanceCache(),
		channels: channels,
	}
}

// Free releases any resources
func (repoDataLoader *RepoDataLoader) Free() {
	log.Info("Freeing RepoDataLoader")

	if repoDataLoader.repo != nil {
		repoDataLoader.repo.Free()
	}
}

// Initialise attempts to access the repository
func (repoDataLoader *RepoDataLoader) Initialise(repoPath string) error {
	log.Infof("Opening repository at %v", repoPath)

	repo, err := git.OpenRepository(repoPath)
	if err != nil {
		return err
	}

	repoDataLoader.repo = repo

	return nil
}

// Path returns the file path location of the repository
func (repoDataLoader *RepoDataLoader) Path() string {
	return repoDataLoader.repo.Path()
}

// Head loads the current HEAD ref
func (repoDataLoader *RepoDataLoader) Head() (oid *Oid, branch *Branch, err error) {
	log.Debug("Loading HEAD")
	ref, err := repoDataLoader.repo.Head()
	if err != nil {
		return
	}

	oid = repoDataLoader.cache.getOid(ref.Target())

	if ref.IsBranch() {
		rawBranch := ref.Branch()
		var branchName string
		branchName, err = rawBranch.Name()
		if err != nil {
			return
		}

		branch = &Branch{
			name: branchName,
			oid:  oid,
		}
	}

	log.Debugf("Loaded HEAD %v", oid)

	return
}

// LoadBranches loads all local branch refs currently in the repository
func (repoDataLoader *RepoDataLoader) LoadBranches() (branches []*Branch, err error) {
	branchIter, err := repoDataLoader.repo.NewBranchIterator(git.BranchAll)
	if err != nil {
		return
	}
	defer branchIter.Free()

	err = branchIter.ForEach(func(branch *git.Branch, branchType git.BranchType) error {
		if repoDataLoader.channels.Exit() {
			return errors.New("Program exiting - Aborting loading branches")
		}

		branchName, err := branch.Name()
		if err != nil {
			return err
		}

		rawOid := branch.Target()

		if rawOid == nil {
			ref, err := branch.Resolve()
			if err != nil {
				return err
			}

			rawOid = ref.Target()
		}

		oid := repoDataLoader.cache.getOid(rawOid)

		newBranch := &Branch{
			oid:      oid,
			name:     branchName,
			isRemote: branch.IsRemote(),
		}

		branches = append(branches, newBranch)
		log.Debugf("Loaded branch %v", newBranch)

		return nil
	})

	return
}

// LocalTags loads all tag refs in the repository
func (repoDataLoader *RepoDataLoader) LocalTags() (tags []*Tag, err error) {
	log.Debug("Loading local tags")

	refIter, err := repoDataLoader.repo.NewReferenceIterator()
	if err != nil {
		return
	}
	defer refIter.Free()

	for {
		ref, err := refIter.Next()
		if err != nil || repoDataLoader.channels.Exit() {
			break
		}

		if !ref.IsRemote() && ref.IsTag() {
			tag, _ := repoDataLoader.repo.LookupTag(ref.Target())
			oid := repoDataLoader.cache.getOid(ref.Target())

			newTag := &Tag{
				oid:  oid,
				name: ref.Shorthand(),
				tag:  tag,
			}
			tags = append(tags, newTag)

			log.Debugf("Loaded tag %v", newTag)
		}
	}

	return
}

// Commits loads all commits for the provided ref and returns a channel from which the loaded commits can be read
func (repoDataLoader *RepoDataLoader) Commits(oid *Oid) (<-chan *Commit, error) {
	log.Debugf("Loading commits for oid %v", oid)

	revWalk, err := repoDataLoader.repo.Walk()
	if err != nil {
		return nil, err
	}

	revWalk.Sorting(git.SortTime)
	if err := revWalk.Push(oid.oid); err != nil {
		return nil, err
	}

	commitCh := make(chan *Commit, rdlCommitBufferSize)

	go func() {
		commitNum := 0
		if err := revWalk.Iterate(func(commit *git.Commit) bool {
			if repoDataLoader.channels.Exit() {
				return false
			}

			commitNum++
			commitCh <- repoDataLoader.cache.getCommit(commit)
			return true
		}); err != nil {
			log.Errorf("Error when iterating over commits for oid %v: %v", oid, err)
		}

		close(commitCh)
		revWalk.Free()
		log.Debugf("Loaded %v commits for oid %v", commitNum, oid)
	}()

	return commitCh, nil
}

// Commit loads a commit for the provided oid (if it points to a commit)
func (repoDataLoader *RepoDataLoader) Commit(oid *Oid) (commit *Commit, err error) {
	object, err := repoDataLoader.repo.Lookup(oid.oid)
	if err != nil {
		log.Debugf("Error when attempting to lookup object with ID %v", oid)
		return
	}

	var rawCommit *git.Commit

	switch object.Type() {
	case git.ObjectCommit:
		rawCommit, err = object.AsCommit()
		if err != nil {
			log.Debugf("Error when attempting convert object with ID %v to commit", oid)
			return
		}
	case git.ObjectTag:
		var tag *git.Tag
		tag, err = object.AsTag()
		if err != nil {
			log.Debugf("Error when attempting convert object with ID %v to tag", oid)
			return
		}

		if tag.TargetType() != git.ObjectCommit {
			err = fmt.Errorf("Tag with ID %v does not point to a commit", oid)
			return
		}

		rawCommit, err = tag.Target().AsCommit()
		if err != nil {
			log.Debugf("Error when attempting convert tag with ID %v to commit", oid)
			return
		}
	default:
		log.Debugf("Unable to convert object with type %v and ID %v to a commit", object.Type().String(), oid)
		return
	}

	commit = repoDataLoader.cache.getCommit(rawCommit)

	return
}

// Diff generates a diff for the provided commit
func (repoDataLoader *RepoDataLoader) Diff(commit *Commit) (diff *Diff, err error) {
	diff = &Diff{}

	if commit.commit.ParentCount() > 1 {
		return
	}

	var commitTree, parentTree *git.Tree
	if commitTree, err = commit.commit.Tree(); err != nil {
		return
	}
	defer commitTree.Free()

	if commit.commit.ParentCount() > 0 {
		if parentTree, err = commit.commit.Parent(0).Tree(); err != nil {
			return
		}
		defer parentTree.Free()
	}

	options, err := git.DefaultDiffOptions()
	if err != nil {
		return
	}

	commitDiff, err := repoDataLoader.repo.DiffTreeToTree(parentTree, commitTree, &options)
	if err != nil {
		return
	}
	defer func() {
		if e := commitDiff.Free(); e != nil {
			log.Errorf("Error when freeing commit diff: %v", e)
		}
	}()

	stats, err := commitDiff.Stats()
	if err != nil {
		return
	}

	statsText, err := stats.String(git.DiffStatsFull, rdlDiffStatsCols)
	if err != nil {
		return
	}

	diff.stats.WriteString(statsText)

	numDeltas, err := commitDiff.NumDeltas()
	if err != nil {
		return
	}

	var patch *git.Patch
	var patchString string

	for i := 0; i < numDeltas; i++ {
		if patch, err = commitDiff.Patch(i); err != nil {
			return
		}

		if patchString, err = patch.String(); err != nil {
			return
		}

		diff.diffText.WriteString(patchString)

		if err := patch.Free(); err != nil {
			log.Errorf("Error when freeing patch: %v", err)
		}
	}

	return
}

// LoadStatus loads git status and populates a Status instance with the data
func (repoDataLoader *RepoDataLoader) LoadStatus() (*Status, error) {
	statusOptions := git.StatusOptions{
		Show:  git.StatusShowIndexAndWorkdir,
		Flags: git.StatusOptIncludeUntracked,
	}

	statusList, err := repoDataLoader.repo.StatusList(&statusOptions)
	if err != nil {
		return nil, fmt.Errorf("Unable to determine repository status: %v", err)
	}

	defer statusList.Free()

	entryCount, err := statusList.EntryCount()
	if err != nil {
		return nil, fmt.Errorf("Unable to determine repository status: %v", err)
	}

	status := newStatus()

	for i := 0; i < entryCount; i++ {
		statusEntry, err := statusList.ByIndex(i)
		if err != nil {
			return nil, fmt.Errorf("Unable to determine repository status: %v", err)
		}

		status.addEntry(statusEntry)
	}

	return status, nil
}
