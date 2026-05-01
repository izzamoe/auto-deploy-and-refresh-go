import { useEffect, useState } from "react";
import { AdminEventProvider } from "./AdminEventProvider";
import { AppForm } from "./AppForm";
import { AppsList } from "./AppsList";
import { HistoryList } from "./HistoryList";

function AdminRouter() {
	const [route, setRoute] = useState(window.location.hash || "#/");
	const [flash, setFlash] = useState<{ message: string, type: "success" | "error" } | null>(null);

	useEffect(() => {
		const handleHashChange = () => {
			setRoute(window.location.hash || "#/");
			
			const searchParams = new URLSearchParams(window.location.search);
			const flashMsg = searchParams.get("flash");
			if (flashMsg) {
				setFlash({ message: flashMsg, type: "success" });
				const newUrl = window.location.pathname + window.location.hash;
				window.history.replaceState({}, "", newUrl);
			}
		};
		handleHashChange();
		window.addEventListener("hashchange", handleHashChange);
		return () => window.removeEventListener("hashchange", handleHashChange);
	}, []);

	const navigate = (hash: string, flashMessage?: string) => {
		if (flashMessage) {
			setFlash({ message: flashMessage, type: "success" });
		}
		window.location.hash = hash;
	};

	return (
		<div data-testid="admin-shell" className="admin-shell">
			<nav id="admin-nav" data-testid="admin-nav">
				<a href="#/" className="brand">Auto Deploy Admin</a>
				<div className="nav-links">
					<a href="#/">Apps</a>
					<a href="#/history">History</a>
				</div>
			</nav>

			<main id="main-content" data-testid="admin-content">
				{flash && (
					<div id="flash" data-testid="admin-flash" className={`flash flash-${flash.type}`}>
						{flash.message}
						<button type="button" className="flash-close" onClick={() => setFlash(null)}>&times;</button>
					</div>
				)}

				<div hidden data-admin-ws-url="/admin/events/ws"></div>

				{route === "#/" || route === "" ? (
					<AppsList navigate={navigate} setFlash={setFlash} />
				) : route === "#/apps/new" ? (
					<AppForm navigate={navigate} setFlash={setFlash} />
				) : route.startsWith("#/apps/") && route.endsWith("/edit") ? (
					<AppForm id={route.split("/")[2]} navigate={navigate} setFlash={setFlash} />
				) : route.startsWith("#/history") ? (
					<HistoryList setFlash={setFlash} />
				) : (
					<div>
						<h2>404 Not Found</h2>
						<a href="#/">Return to Apps</a>
					</div>
				)}
			</main>
		</div>
	);
}

export default function App() {
	return (
		<AdminEventProvider>
			<AdminRouter />
		</AdminEventProvider>
	);
}
