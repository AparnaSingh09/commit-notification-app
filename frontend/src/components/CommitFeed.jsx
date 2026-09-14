import { useEffect, useRef, useState } from 'react'
import { listCommits, listRepos } from '../api/client'

function formatTimestamp(iso) {
  return new Date(iso).toLocaleString()
}

function repoKey(workspace, repoSlug) {
  return `${workspace}/${repoSlug}`
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

// selectedKeys is null for "All" (the default, never persisted - resets to
// All on every page load/re-login) or an array of "workspace/repoSlug" keys.
function RepoFilter({ repos, selectedKeys, onChange }) {
  const [open, setOpen] = useState(false)
  const ref = useRef(null)

  useEffect(() => {
    function handleClickOutside(e) {
      if (ref.current && !ref.current.contains(e.target)) {
        setOpen(false)
      }
    }
    document.addEventListener('mousedown', handleClickOutside)
    return () => document.removeEventListener('mousedown', handleClickOutside)
  }, [])

  if (repos.length === 0) return null

  const isAll = selectedKeys === null

  function toggleRepo(key) {
    if (isAll) {
      onChange([key])
      return
    }
    const next = selectedKeys.includes(key)
      ? selectedKeys.filter((k) => k !== key)
      : [...selectedKeys, key]
    onChange(next.length === 0 ? null : next)
  }

  let label = 'All repos'
  if (!isAll) {
    label = selectedKeys.length === 1 ? selectedKeys[0] : `${selectedKeys.length} repos selected`
  }

  return (
    <div className="repo-filter" ref={ref}>
      <button type="button" className="btn btn-secondary" onClick={() => setOpen((o) => !o)}>
        {label} <span className="repo-filter-caret">▾</span>
      </button>
      {open && (
        <div className="repo-filter-menu">
          <label className="repo-filter-option">
            <input type="checkbox" checked={isAll} onChange={() => onChange(null)} />
            All repos
          </label>
          <div className="repo-filter-divider" />
          {repos.map((r) => {
            const key = repoKey(r.workspace, r.repoSlug)
            return (
              <label key={r.id} className="repo-filter-option">
                <input
                  type="checkbox"
                  checked={!isAll && selectedKeys.includes(key)}
                  onChange={() => toggleRepo(key)}
                />
                {key}
              </label>
            )
          })}
        </div>
      )}
    </div>
  )
}

function CommitFeed() {
  const [commits, setCommits] = useState([])
  const [repos, setRepos] = useState([])
  // null = "All" - the default every time this component mounts (login,
  // reload, etc.); deliberately not persisted anywhere.
  const [selectedRepoKeys, setSelectedRepoKeys] = useState(null)
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
    listRepos()
      .then(setRepos)
      .catch(() => {
        /* filter dropdown just won't show if this fails - not fatal to the feed itself */
      })
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [])

  async function handleLoadMore() {
    setLoadingMore(true)
    await load(nextCursor)
  }

  const visibleCommits =
    selectedRepoKeys === null
      ? commits
      : commits.filter((c) => selectedRepoKeys.includes(repoKey(c.workspace, c.repoSlug)))

  return (
    <section className="card">
      <div className="card-body">
        <div className="card-header">
          <h2>Commit Feed</h2>
          <RepoFilter repos={repos} selectedKeys={selectedRepoKeys} onChange={setSelectedRepoKeys} />
        </div>

        {error && <p className="form-error">{error}</p>}

        {loaded && commits.length === 0 && !error && (
          <p className="empty-state">No commits yet. New commits on your subscribed repos will show up here.</p>
        )}

        {loaded && commits.length > 0 && visibleCommits.length === 0 && (
          <p className="empty-state">No commits from the selected repo(s) in what's loaded so far - try Load more.</p>
        )}

        <ul className="list">
          {visibleCommits.map((c) => (
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
