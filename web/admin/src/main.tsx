import ReactDOM from 'react-dom/client';
import { ThemeProvider } from 'next-themes';
import './index.css';
import App from './App';

ReactDOM.createRoot(document.getElementById('root') as HTMLElement).render(
  <ThemeProvider attribute="class" defaultTheme="system" enableSystem disableTransitionOnChange>
    <App />
  </ThemeProvider>
);
