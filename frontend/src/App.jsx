import { useEffect, useState } from 'react'
import { getMe, loginUrl, logout } from './api/client'
import ManageRepos from './components/ManageRepos'
import CommitFeed from './components/CommitFeed'
import './App.css'

function App() {
  const [user, setUser] = useState(null)
  const [loading, setLoading] = useState(true)

  useEffect(() => {
    getMe()
      .then(setUser)
      .finally(() => setLoading(false))
  }, [])

  async function handleLogout() {
    await logout()
    setUser(null)
  }

  if (loading) {
    return (
      <div className="page-loading">
        <p>Loading...</p>
      </div>
    )
  }

  if (!user) {
    return (
      <div className="login-screen">
        <div className="card login-card">
          <div className="card-body">
            <h1>Commit Notification App</h1>
            <p>Log in with your Bitbucket account to subscribe to repos and see a feed of new commits.</p>
            <a className="btn btn-primary btn-block" href={loginUrl()}>
              Login with Bitbucket
            </a>
          </div>
        </div>
      </div>
    )
  }

  return (
    <div className="app">
      <header className="app-header">
        <div className="brand">
          <span className="brand-dot" />
          Commit Notification App
        </div>
        <div className="user">
          {user.avatarUrl && <img className="avatar" src={user.avatarUrl} alt="" />}
          <span>{user.displayName || user.username}</span>
          <button type="button" className="btn btn-secondary" onClick={handleLogout}>
            Log out
          </button>
        </div>
      </header>

      <main className="app-main">
        <ManageRepos />
        <CommitFeed />
      </main>
    </div>
  )
}

export default App
