import { Component, StrictMode, type ErrorInfo, type ReactNode } from 'react'
import { createRoot } from 'react-dom/client'

import App from './App'
import './style.css'

class ErrorBoundary extends Component<{ children: ReactNode }, { error?: Error }> {
  state: { error?: Error } = {}

  static getDerivedStateFromError(error: Error) {
    return { error }
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    console.error('wtable desktop render failed', error, info)
  }

  render() {
    if (!this.state.error) return this.props.children
    return (
      <main className="launch-screen error-screen">
        <div className="launch-mark warning"><span>!</span></div>
        <div className="launch-copy">
          <strong>界面加载失败</strong>
          <p>{this.state.error.message}</p>
        </div>
        <button className="primary-button" onClick={() => window.location.reload()}>
          重新加载
        </button>
      </main>
    )
  }
}

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <ErrorBoundary>
      <App />
    </ErrorBoundary>
  </StrictMode>,
)
