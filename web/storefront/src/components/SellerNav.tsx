import { NavLink } from "react-router-dom";

const TABS = [
  { to: "/seller", label: "Shop", end: true },
  { to: "/seller/products", label: "Products" },
  { to: "/seller/staff", label: "Staff" },
  { to: "/seller/payouts", label: "Payouts" },
] as const;

export function SellerNav() {
  return (
    <nav className="seller-nav">
      {TABS.map((t) => (
        <NavLink key={t.to} to={t.to} end={"end" in t && t.end} className={({ isActive }) => (isActive ? "active" : "")}>
          {t.label}
        </NavLink>
      ))}
    </nav>
  );
}
