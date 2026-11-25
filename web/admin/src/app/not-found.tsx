// Becomes 404.html in the export; the Go handler serves it with status 404 (D1).
export default function NotFound() {
  return (
    <main data-testid="not-found">
      <h1>404</h1>
      <p>Page not found</p>
      <a href="/admin/">auto-deploy</a>
    </main>
  );
}
