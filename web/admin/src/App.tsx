import { useCallback, useEffect, useState } from "react";
import { AdminEventProvider } from "./AdminEventProvider";
import { AppForm } from "./AppForm";
import { AppsList } from "./AppsList";
import { HistoryList } from "./HistoryList";
import { AccountSettings, ForcePasswordChange } from "./Account";
import { getAccount, type Account } from "./api";
import { TelegramSettings } from "./TelegramSettings";
import { GitHubSettings } from "./GitHubSettings";

import { Button } from "@/components/ui/button";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { Separator } from "@/components/ui/separator";

function AdminRouter({ account, refreshAccount }: { account: Account | null; refreshAccount: () => void }) {
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
		<div data-testid="admin-shell" className="min-h-screen bg-background flex flex-col font-sans">
			<nav id="admin-nav" data-testid="admin-nav" className="flex items-center justify-between px-6 py-4">
				<a href="#/" className="font-semibold text-lg tracking-tight">Auto Deploy Admin</a>
				<div className="flex gap-2">
					<Button variant="ghost" asChild>
						<a href="#/">Apps</a>
					</Button>
					<Button variant="ghost" asChild>
						<a href="#/history">History</a>
					</Button>
					<Button variant="ghost" asChild>
						<a href="#/account">Account</a>
					</Button>
					<Button variant="ghost" asChild>
						<a href="#/telegram">Telegram</a>
					</Button>
					<Button variant="ghost" asChild>
						<a href="#/github">GitHub</a>
					</Button>
				</div>
			</nav>

			<Separator />

			<main id="main-content" data-testid="admin-content" className="flex-1 p-6 container mx-auto max-w-5xl">
				{flash && (
					<Alert data-testid="admin-flash" variant={flash.type === "error" ? "destructive" : "default"} className="mb-6 flex items-center justify-between py-3">
						<AlertDescription className="text-sm font-medium">
							{flash.message}
						</AlertDescription>
						<Button 
							type="button" 
							variant="ghost" 
							size="icon" 
							onClick={() => setFlash(null)} 
							aria-label="Dismiss notification"
							className="h-6 w-6 rounded-md hover:bg-muted"
						>
							<span aria-hidden="true">&times;</span>
						</Button>
					</Alert>
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
				) : route.startsWith("#/account") ? (
					<AccountSettings username={account?.username ?? ""} onUpdated={refreshAccount} setFlash={setFlash} />
				) : route.startsWith("#/telegram") ? (
					<TelegramSettings setFlash={setFlash} />
				) : route.startsWith("#/github") ? (
					<GitHubSettings setFlash={setFlash} />
				) : (
					<div className="flex flex-col items-center justify-center py-12 text-center">
						<h2 className="text-2xl font-bold mb-4">404 Not Found</h2>
						<Button asChild>
							<a href="#/">Return to Apps</a>
						</Button>
					</div>
				)}
			</main>
		</div>
	);
}

export default function App() {
	const [account, setAccount] = useState<Account | null>(null);
	const [accountLoaded, setAccountLoaded] = useState(false);

	const refreshAccount = useCallback(() => {
		getAccount()
			.then(setAccount)
			.catch(() => setAccount(null))
			.finally(() => setAccountLoaded(true));
	}, []);

	useEffect(() => {
		refreshAccount();
	}, [refreshAccount]);

	// Wait for the account probe before mounting anything that talks to the
	// gated data/event API. Otherwise the force-change gate answers the first
	// AppsList/WebSocket calls with 403 — leaving a stale "Password change
	// required" flash and a reconnecting WebSocket behind the change screen.
	if (!accountLoaded) {
		return null;
	}

	// Block the whole UI on the forced password change until the seeded default
	// (admin/11) is replaced. The event WebSocket stays unmounted here — it is
	// gated server-side and would just 403-loop.
	if (account?.mustChangePassword) {
		return <ForcePasswordChange onDone={refreshAccount} />;
	}

	return (
		<AdminEventProvider>
			<AdminRouter account={account} refreshAccount={refreshAccount} />
		</AdminEventProvider>
	);
}