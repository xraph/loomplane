import { RootProvider } from "fumadocs-ui/provider/next";
import "./global.css";
import type { Metadata, Viewport } from "next";
import { Geist, Geist_Mono } from "next/font/google";
import { Sprite } from "@/components/landing/sprite";
import { appName } from "@/lib/shared";

const sans = Geist({ subsets: ["latin"], variable: "--font-geist-sans" });
const mono = Geist_Mono({ subsets: ["latin"], variable: "--font-geist-mono" });

const description =
  "A self-hosted deployment platform for the monorepos you already have.";

// Set NEXT_PUBLIC_SITE_URL where the site is served, so social previews get absolute URLs.
const siteUrl = process.env.NEXT_PUBLIC_SITE_URL ?? "https://loomplane.dev";

export const metadata: Metadata = {
  metadataBase: new URL(siteUrl),
  title: { template: `%s | ${appName}`, default: appName },
  description,
  applicationName: appName,
  appleWebApp: { title: appName },
  openGraph: {
    type: "website",
    siteName: appName,
    title: appName,
    description,
  },
  twitter: { card: "summary_large_image", title: appName, description },
};

export const viewport: Viewport = {
  colorScheme: "dark light",
  themeColor: [
    { media: "(prefers-color-scheme: dark)", color: "#0a0a0a" },
    { media: "(prefers-color-scheme: light)", color: "#ffffff" },
  ],
};

export default function Layout({ children }: LayoutProps<"/">) {
  return (
    <html
      lang="en"
      className={`${sans.className} ${sans.variable} ${mono.variable}`}
      suppressHydrationWarning
    >
      <body className="flex flex-col min-h-screen">
        <Sprite />
        <RootProvider theme={{ defaultTheme: "dark" }}>{children}</RootProvider>
      </body>
    </html>
  );
}
