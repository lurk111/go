"""Synthetic fixture shape and identifiers; no monitoring implementation."""
from datetime import UTC, datetime
import re

LIST_PATHS = (
    "business_requirements.items",
    "technical_requirements.items",
    "qualification_review",
    "conformity_review",
    "waste_items",
    "score_requirements",
    "bid_evaluation_requirements",
    "bid_document_components",
)
INSTRUCTION_FIELDS = frozenset(
    (
        "project_name project_number package_number subpackage_number section_number section_name package_name "
        "subpackage_name budget tenderee tender_agency project_domain project_summary for_sme bid_deadline "
        "bid_opening_time bid_opening_place bidding_method bid_validity_period consortium_allowed subcontract_allowed "
        "transfer_allowed bond_amount bond_account bond_payment_method bond_payment_remark blind_bid"
    ).split()
)



TASK = re.compile(r"[A-Za-z0-9][A-Za-z0-9_-]{0,63}\Z")

def utcnow():
    return datetime.now(UTC)
