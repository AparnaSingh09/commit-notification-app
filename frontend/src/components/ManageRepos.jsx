import { useEffect, useState } from 'react'
import { addRepo, listRepos, removeRepo } from '../api/client'

function ManageRepos() {
  const [repos, setRepos] = useState([])
  const [workspace, setWorkspace] = useState('')
  const [repoSlug, setRepoSlug] = useState('')
  const [error, setError] = useState('')
  const [submitting, setSubmitting] = useState(false)

  function refresh() {
    listRepos()
      .then(setRepos)
      .catch(() => setError('could not load your repos'))
  }

  useEffect(refresh, [])

  async function handleSubmit(e) {
    e.preventDefault()
    setError('')
    setSubmitting(true)
    try {
      await addRepo(workspace.trim(), repoSlug.trim())
      setWorkspace('')
      setRepoSlug('')
      refresh()
    } catch (err) {
      setError(err.message)
    } finally {
      setSubmitting(false)
    }
  }

  async function handleRemove(id) {
    try {
      await removeRepo(id)
      refresh()
    } catch {
      setError('could not remove repo')
    }
  }

  return (
    <section className="card">
      <div className="card-body">
        <div className="card-header">
          <h2>Manage Repos</h2>
          <span className="count">{repos.length} subscribed</span>
        </div>

        <form className="repo-form" onSubmit={handleSubmit}>
          <div className="repo-inputs">
            <input
              type="text"
              placeholder="workspace"
              value={workspace}
              onChange={(e) => setWorkspace(e.target.value)}
              required
            />
            <span className="slash">/</span>
            <input
              type="text"
              placeholder="repo-slug"
              value={repoSlug}
              onChange={(e) => setRepoSlug(e.target.value)}
              required
            />
          </div>
          <button type="submit" className="btn btn-primary" disabled={submitting}>
            {submitting ? 'Adding...' : 'Add repo'}
          </button>
        </form>

        {error && <p className="form-error">{error}</p>}

        {repos.length === 0 ? (
          <p className="empty-state">No repos subscribed yet — add one above.</p>
        ) : (
          <ul className="list">
            {repos.map((r) => (
              <li key={r.id}>
                <div className="repo-row">
                  <span className="repo-name">{r.workspace}/{r.repoSlug}</span>
                  <button type="button" className="btn btn-ghost-danger" onClick={() => handleRemove(r.id)}>
                    Remove
                  </button>
                </div>
              </li>
            ))}
          </ul>
        )}
      </div>
    </section>
  )
}

export default ManageRepos
