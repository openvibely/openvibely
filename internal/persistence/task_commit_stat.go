package persistence

type ProducedCommitStatAggregate struct {
	TotalCommits    int
	TotalInsertions int
	TotalDeletions  int
}
