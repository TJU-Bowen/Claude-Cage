// Convert the entry into a real Expo Router page, using the same sidebar and header as History.
// The desktop packager supplies these components from its pinned Paseo release.
export function createCageNavigation({ React, router, usePathname, useIsFocused, useUnistyles, SidebarHeaderRow, MenuHeader, ServerIcon }) {
  const path = "/cage-servers";
  const label = "局域网服务器";
  const item = { kind: "builtin", key: "cage-servers", id: "cage-servers" };

  function navigate() {
    router.navigate(path);
  }
  window.cageWorkstations?.onShow(navigate);

  function SidebarRow({ onBeforeNavigate }) {
    const active = usePathname() === path;
    return React.createElement(SidebarHeaderRow, {
      label, icon: ServerIcon, variant: "compact", isActive: active, testID: "sidebar-cage-servers",
      onPress() {
        onBeforeNavigate?.();
        if (!active) navigate();
      }
    });
  }

  function ServerScreen() {
    const focused = useIsFocused();
    const { theme } = useUnistyles();
    React.useEffect(() => {
      if (!focused) return;
      const returnToWorkspace = () => {
        if (router.canGoBack()) router.back();
        else router.replace("/");
      };
      window.addEventListener("cage-workbench-opened", returnToWorkspace);
      return () => window.removeEventListener("cage-workbench-opened", returnToWorkspace);
    }, [focused]);

    const colors = theme.colors;
    const style = {
      "--station-surface": colors.surface0, "--station-card": colors.surface1,
      "--station-hover": colors.surface2, "--station-border": colors.border,
      "--station-text": colors.foreground, "--station-muted": colors.foregroundMuted,
      "--station-accent": colors.accent, "--station-on-accent": colors.accentForeground,
    };
    return React.createElement("section", { className: "cage-server-page", style, "aria-label": label },
      React.createElement(MenuHeader, { title: label }),
      focused && React.createElement("div", { className: "cage-server-scroll" },
        React.createElement("cage-server-list")
      )
    );
  }

  return {
    __esModule: true, default: ServerScreen, SidebarRow,
    withSidebarItem(items) {
      return window.cageWorkstations ? [...items, item] : items;
    },
  };
}
