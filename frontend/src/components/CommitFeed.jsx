import { useEffect, useState } from 'react'
import { listCommits } from '../api/client'

function formatTimestamp(iso) {
  return new Date(iso).toLocaleString()
}

function CommitRow({ commit }) {
  return (
    <li>
      <div className="commit-row">
        {commit.summaryStatus === 'done' ? (
          <div className="commit-summary">{commit.aiSummary}</div>
        ) : (
          <div className="commit-summary is-muted">
            <span className={`badge ${commit.summaryStatus === 'failed' ? 'badge-failed' : 'badge-pending'}`}>
              {commit.summaryStatus === 'failed' ? 'summary failed' : 'summarizing...'}
            </span>
          </div>
        )}

        <div className="commit-message">
          {commit.workspace}/{commit.repoSlug} <span className="sep">—</span> {commit.message.trim()}
        </div>

        <div className="commit-meta">
          <span>{commit.authorName || 'unknown author'}</span>
          <span className="sep">·</span>
          <span>{formatTimestamp(commit.committedAt)}</span>
          <span className="sep">·</span>
          <a href={commit.bitbucketUrl} target="_blank" rel="noreferrer">view on Bitbucket</a>
        </div>
      </div>
    </li>
  )
}

function CommitFeed() {
  const [commits, setCommits] = useState([])
  const [nextCursor, setNextCursor] = useState('')
  const [loaded, setLoaded] = useState(false)
  const [error, setError] = useState('')
  const [loadingMore, setLoadingMore] = useState(false)

  async function load(before) {
    try {
      const res = await listCommits(before)
      setCommits((prev) => (before ? [...prev, ...res.commits] : res.commits))
      setNextCursor(res.nextCursor)
    } catch {
      setError('could not load the commit feed')
    } finally {
      setLoaded(true)
      setLoadingMore(false)
    }
  }

  useEffect(() => {
    load()
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  async function handleLoadMore() {
    setLoadingMore(true)
    await load(nextCursor)
  }

  return (
    <section className="card">
      <div className="card-body">
        <div className="card-header">
          <h2>Commit Feed</h2>
        </div>

        {error && <p className="form-error">{error}</p>}

        {loaded && commits.length === 0 && !error && (
          <p className="empty-state">No commits yet. New commits on your subscribed repos will show up here.</p>
        )}

        <ul className="list">
          {commits.map((c) => (
            <CommitRow key={c.id} commit={c} />
          ))}
        </ul>

        {nextCursor && (
          <div className="load-more">
            <button type="button" className="btn btn-secondary" onClick={handleLoadMore} disabled={loadingMore}>
              {loadingMore ? 'Loading...' : 'Load more'}
            </button>
          </div>
        )}
      </div>
    </section>
  )
}

export default CommitFeed
