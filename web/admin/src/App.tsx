import { useCallback, useEffect, useState } from "react";
import { GitBranch, History, LayoutGrid, Rocket, Send, UserCog } from "lucide-react";
import { AdminEventProvider } from "./AdminEventProvider";
import { AppForm } from "./AppForm";
import { AppsList } from "./AppsList";
import { HistoryList } from "./HistoryList";
import { AccountSettings, ForcePasswordChange } from "./Account";
import { getAccount, type Account } from "./api";
import { TelegramSettings } from "./TelegramSettings";
import { GitHubSettings } from "./GitHubSettings";
import { ThemeToggle } from "./ThemeToggle";

import { Button } from "@/components/ui/button";
import { Alert, AlertDescription } from "@/components/ui/alert";
import { cn } from "@/lib/utils";

const NAV_ITEMS = [
	{ label: "Apps", href: "#/", icon: LayoutGrid, match: (r: string) => r === "" || r === "#/" || r.startsWith("#/apps") },
	{ label: "History", href: "#/history", icon: History, match: (r: string) => r.startsWith("#/history") },
	{ label: "Account", href: "#/account", icon: UserCog, match: (r: string) => r.startsWith("#/account") },
	{ label: "Telegram", href: "#/telegram", icon: Send, match: (r: string) => r.startsWith("#/telegram") },
	{ label: "GitHub", href: "#/github", icon: GitBranch, match: (r: string) => r.startsWith("#/github") },
];

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

	const activeItem = NAV_ITEMS.find((item) => item.match(route));

	return (
		<div data-testid="admin-shell" className="flex min-h-screen flex-col bg-background font-sans text-foreground md:flex-row">
			<aside
				id="admin-nav"
				data-testid="admin-nav"
				className="flex shrink-0 flex-col gap-5 border-b border-border/60 bg-card/40 p-4 md:sticky md:top-0 md:h-screen md:w-64 md:border-b-0 md:border-r"
			>
				<a href="#/" className="flex items-center gap-2.5 px-2 py-1 text-base font-semibold tracking-tight">
					<span className="flex size-8 items-center justify-center rounded-lg bg-primary text-primary-foreground shadow-sm">
						<Rocket className="size-4" />
					</span>
					Auto Deploy Admin
				</a>

				<nav className="flex gap-1 overflow-x-auto pb-1 md:flex-col md:overflow-visible md:pb-0">
					{NAV_ITEMS.map((item) => {
						const Icon = item.icon;
						const active = item.match(route);
						return (
							<Button
								key={item.href}
								asChild
								variant={active ? "secondary" : "ghost"}
								className={cn("shrink-0 justify-start gap-2.5", active && "font-medium text-foreground")}
							>
								<a href={item.href}>
									<Icon className="size-4 shrink-0 opacity-80" />
									{item.label}
								</a>
							</Button>
						);
					})}
				</nav>

				<p className="mt-auto hidden px-2 text-xs text-muted-foreground md:block">
					Automated GitHub release deployments
				</p>
			</aside>

			<div className="flex min-w-0 flex-1 flex-col">
				<header className="sticky top-0 z-10 flex h-16 items-center justify-between gap-4 border-b border-border/60 bg-background/80 px-6 backdrop-blur">
					<h1 className="text-sm font-medium text-muted-foreground">{activeItem?.label ?? "Admin"}</h1>
					<ThemeToggle />
				</header>

				<main id="main-content" data-testid="admin-content" className="flex-1 p-4 sm:p-6 lg:p-8">
					<div className="mx-auto w-full max-w-5xl">
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
									className="size-6 rounded-md hover:bg-muted"
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
								<h2 className="mb-4 text-2xl font-bold">404 Not Found</h2>
								<Button asChild>
									<a href="#/">Return to Apps</a>
								</Button>
							</div>
						)}
					</div>
				</main>
			</div>
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